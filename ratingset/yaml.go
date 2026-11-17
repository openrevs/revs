package ratingset

import (
	"bytes"
	"crypto/ed25519"

	"github.com/go-playground/validator/v10"
	jsonlines "github.com/openrevs/sjsonl-go"
	"gopkg.in/yaml.v2"
)

// Example is a ratingset written as YAML (see data.yaml), a convenient
// hand-edited source for a document.
type Example struct {
	Author Author
	Items  []Item
}

// ConvertYAML parses and validates a YAML ratingset and returns it as a
// JSONL document signed with privateKey.
func ConvertYAML(data []byte, privateKey ed25519.PrivateKey) ([]byte, error) {
	var conf Example
	if err := yaml.Unmarshal(data, &conf); err != nil {
		return nil, err
	}

	validate := validator.New()
	if err := validate.Struct(conf); err != nil {
		return nil, err
	}

	var buf bytes.Buffer

	d, err := EncodeLine(conf.Author)
	if err != nil {
		return nil, err
	}
	buf.Write(d)
	buf.WriteByte('\n')

	for _, item := range conf.Items {
		d, err = EncodeLine(item.Normalize())
		if err != nil {
			return nil, err
		}
		buf.Write(d)
		buf.WriteByte('\n')
	}

	signed, _, err := jsonlines.Sign(buf.Bytes(), privateKey)
	return signed, err
}
