package api

import "embed"

//go:embed openapi.json
var specFS embed.FS

func Spec() []byte {
	b, _ := specFS.ReadFile("openapi.json")
	return b
}
