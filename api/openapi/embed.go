// Package openapi embeds the hr browser API contract.
package openapi

import _ "embed"

// HR is the OpenAPI 3.1 document served and validated by the service.
//
//go:embed hr.yaml
var HR []byte
