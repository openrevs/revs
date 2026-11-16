package revs

import (
	"bytes"
	"crypto/ed25519"
	"fmt"

	"github.com/go-playground/validator/v10"
	jsonlines "github.com/openrevs/sjsonl-go"
	"gopkg.in/yaml.v2"
)

type Example struct {
	Author Author
	Items  []Item
}

func ConvertYAML(data, privateKey []byte) (*Example, error) {
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

	signed, _, err := jsonlines.Sign(buf.Bytes(), ed25519.PrivateKey(privateKey))
	if err != nil {
		return nil, err
	}

	fmt.Print(string(signed))

	return &conf, nil
}
