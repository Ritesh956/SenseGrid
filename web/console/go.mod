// This module boundary exists for exactly one reason: some npm packages
// (observed: flatted, pulled in transitively) ship a stray .go file inside
// node_modules. Without a go.mod here, the root module's `go build ./...`
// (and go vet/go test — see CLAUDE.md's documented commands) walks into
// web/console/node_modules and picks that file up as part of the SenseGrid
// module, which is slow and pointless since web/console has no real Go
// code of its own. A go.mod here — even one nothing ever imports — marks
// this whole subtree as a separate module, which the root module's `...`
// pattern skips automatically. Found live after `npm install` in this
// directory made `go test ./...` sweep node_modules; not theoretical.
module sensegrid-console-go-boundary

go 1.25
