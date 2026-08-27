// Package dynsec is a minimal client for Mosquitto's dynamic-security
// plugin control protocol: an MQTT-based RPC used to create per-device
// broker credentials and roles at runtime, with no broker restart.
//
// Wire protocol (verified against eclipse-mosquitto/mosquitto source,
// plugins/dynamic-security/control.c and src/control_common.c):
//
//	request  -> publish {"commands":[{"command":"...", "correlationData":"...", ...}]}
//	           to $CONTROL/dynamic-security/v1
//	response <- {"responses":[{"command":"...","correlationData":"...","error":"..."}]}
//	           on $CONTROL/dynamic-security/v1/response, error present only on failure
package dynsec

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/google/uuid"

	"github.com/Ritesh956/SenseGrid/internal/tlsutil"
)

const (
	commandTopic  = "$CONTROL/dynamic-security/v1"
	responseTopic = "$CONTROL/dynamic-security/v1/response"

	// Returned by the plugin when the target already exists; treated as
	// success by the Ensure* helpers so bootstrap is safe to run on every
	// control-plane startup.
	errRoleExists   = "Role already exists"
	errClientExists = "Client already exists"
)

// ACL is one entry in a role's access list. ACLType is one of
// publishClientSend, publishClientReceive, subscribeLiteral,
// subscribePattern, unsubscribeLiteral, unsubscribePattern. Topic may use
// %c (client id) and %u (username) substitution.
type ACL struct {
	ACLType  string `json:"acltype"`
	Topic    string `json:"topic"`
	Priority int    `json:"priority"`
	Allow    bool   `json:"allow"`
}

type roleRef struct {
	RoleName string `json:"rolename"`
	Priority int    `json:"priority"`
}

type response struct {
	Command         string `json:"command"`
	CorrelationData string `json:"correlationData"`
	Error           string `json:"error"`
}

type responseEnvelope struct {
	Responses []response `json:"responses"`
}

// Config configures the admin connection used to issue dynsec commands.
type Config struct {
	BrokerURL string // e.g. tls://mosquitto:8883
	CAFile    string
	Username  string
	Password  string
	Timeout   time.Duration // per-command RPC timeout; defaults to 10s
}

// Client is a connected dynsec admin session.
type Client struct {
	mqtt    mqtt.Client
	timeout time.Duration

	mu      sync.Mutex
	pending map[string]chan response
}

// Connect dials the broker as the dynsec admin user and subscribes to the
// response topic. The returned Client must be closed with Disconnect.
func Connect(cfg Config) (*Client, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	c := &Client{
		timeout: cfg.Timeout,
		pending: make(map[string]chan response),
	}

	opts := mqtt.NewClientOptions().
		AddBroker(cfg.BrokerURL).
		SetClientID("sensegrid-control-dynsec-admin").
		SetUsername(cfg.Username).
		SetPassword(cfg.Password).
		SetConnectTimeout(cfg.Timeout).
		SetAutoReconnect(true).
		SetOnConnectHandler(func(mc mqtt.Client) {
			// Fire-and-forget is fine here: this handler only matters for
			// surviving a *later* reconnect (clean sessions, the default,
			// don't persist subscriptions across a dropped connection).
			// The synchronous Subscribe below is what guarantees the
			// first command a caller issues right after Connect()
			// returns isn't lost.
			mc.Subscribe(responseTopic, 1, c.handleResponse)
		})

	if cfg.CAFile != "" {
		tlsCfg, err := tlsutil.FromCAFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("dynsec: loading CA: %w", err)
		}
		opts.SetTLSConfig(tlsCfg)
	}

	c.mqtt = mqtt.NewClient(opts)
	token := c.mqtt.Connect()
	if !token.WaitTimeout(cfg.Timeout) {
		return nil, fmt.Errorf("dynsec: connect timed out after %s", cfg.Timeout)
	}
	if err := token.Error(); err != nil {
		return nil, fmt.Errorf("dynsec: connect: %w", err)
	}

	// Subscribe synchronously before returning: found live (repeatedly,
	// not a one-off) that without this, a caller issuing a command
	// immediately after Connect() returns — which every caller does —
	// races the OnConnectHandler's own async subscribe above. If
	// mosquitto processes and responds to the command before that
	// subscribe reaches the broker, the response gets published while
	// this client isn't listening yet, and the command times out with
	// nothing in the logs to explain why (it looks identical to the
	// broker just being slow). This is what cmd/control/main.go's
	// connectDynsec retry loop was actually working around — retrying
	// helped because each attempt is a fresh race, not because the
	// broker needed more time; this fixes the race directly instead.
	subToken := c.mqtt.Subscribe(responseTopic, 1, c.handleResponse)
	if !subToken.WaitTimeout(cfg.Timeout) {
		c.mqtt.Disconnect(250)
		return nil, fmt.Errorf("dynsec: subscribing to %s timed out", responseTopic)
	}
	if err := subToken.Error(); err != nil {
		c.mqtt.Disconnect(250)
		return nil, fmt.Errorf("dynsec: subscribing to %s: %w", responseTopic, err)
	}
	return c, nil
}

// Disconnect closes the admin connection.
func (c *Client) Disconnect() {
	c.mqtt.Disconnect(250)
}

func (c *Client) handleResponse(_ mqtt.Client, msg mqtt.Message) {
	var env responseEnvelope
	if err := json.Unmarshal(msg.Payload(), &env); err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range env.Responses {
		if ch, ok := c.pending[r.CorrelationData]; ok {
			ch <- r
			delete(c.pending, r.CorrelationData)
		}
	}
}

// do publishes a single command and waits for its correlated response.
func (c *Client) do(ctx context.Context, command map[string]any) (response, error) {
	correlationID := uuid.NewString()
	command["correlationData"] = correlationID

	respCh := make(chan response, 1)
	c.mu.Lock()
	c.pending[correlationID] = respCh
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, correlationID)
		c.mu.Unlock()
	}()

	payload, err := json.Marshal(map[string]any{"commands": []any{command}})
	if err != nil {
		return response{}, fmt.Errorf("dynsec: marshal command: %w", err)
	}

	token := c.mqtt.Publish(commandTopic, 1, false, payload)
	if !token.WaitTimeout(c.timeout) {
		return response{}, fmt.Errorf("dynsec: publish %q timed out", command["command"])
	}
	if err := token.Error(); err != nil {
		return response{}, fmt.Errorf("dynsec: publish %q: %w", command["command"], err)
	}

	select {
	case r := <-respCh:
		return r, nil
	case <-time.After(c.timeout):
		return response{}, fmt.Errorf("dynsec: %q: no response within %s", command["command"], c.timeout)
	case <-ctx.Done():
		return response{}, ctx.Err()
	}
}

// EnsureRole creates rolename with the given ACLs if it does not already
// exist. If the role already exists its ACLs are left untouched — dynsec
// has no "replace ACLs" call, and re-adding the same ACL entries on every
// startup would duplicate them, so reconciling an existing role's ACLs is
// left as a deliberate non-goal for now.
func (c *Client) EnsureRole(ctx context.Context, rolename, textname string, acls []ACL) error {
	resp, err := c.do(ctx, map[string]any{
		"command":  "createRole",
		"rolename": rolename,
		"textname": textname,
		"acls":     acls,
	})
	if err != nil {
		return err
	}
	if resp.Error != "" && resp.Error != errRoleExists {
		return fmt.Errorf("dynsec: createRole %q: %s", rolename, resp.Error)
	}
	return nil
}

// CreateClient provisions a new broker credential for a device: username
// and clientid are both set to deviceID (so ACL patterns using %c work),
// with password and the given role assigned.
func (c *Client) CreateClient(ctx context.Context, deviceID, password, textname, rolename string) error {
	resp, err := c.do(ctx, map[string]any{
		"command":  "createClient",
		"username": deviceID,
		"password": password,
		"clientid": deviceID,
		"textname": textname,
		"roles":    []roleRef{{RoleName: rolename, Priority: -1}},
	})
	if err != nil {
		return err
	}
	if resp.Error != "" && resp.Error != errClientExists {
		return fmt.Errorf("dynsec: createClient %q: %s", deviceID, resp.Error)
	}
	return nil
}
