package revs

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"

	"github.com/go-playground/validator/v10"
	jsonlines "github.com/openrevs/sjsonl-go"
	"gopkg.in/yaml.v2"
)

type User2 struct {
	ID        string  `json:"id" validate:"required"`
	Email     *string `json:"email,omitempty"`
	MinRating int     `json:"min_rating"`
	MaxRating int     `json:"max_rating"`
	Revision  int     `json:"revision,omitempty"`
}

type Example struct {
	User   User2
	Titles []Title2
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

	d, err := json.Marshal(conf.User)
	if err != nil {
		return nil, err
	}
	buf.Write(d)
	buf.WriteByte('\n')

	for _, title := range conf.Titles {
		d, err = json.Marshal(title)
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
