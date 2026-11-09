package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	jsonlines "github.com/openrevs/sjsonl-go"
	"github.com/stretchr/testify/require"
)

func TestSignDocument(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	data := []byte("{\"id\":\"x\",\"revision\":1}\n{\"name\":\"Alien\",\"year\":1979}\n")

	signed, err := signDocument(data, key)
	require.NoError(t, err)
	info, err := jsonlines.Verify(signed)
	require.NoError(t, err)

	// Re-signing a signed document replaces its control line.
	again, err := signDocument(signed, key)
	require.NoError(t, err)
	require.Equal(t, signed, again)

	// So an edited signed document can be signed afresh.
	edited := bytes.Replace(signed, []byte("Alien"), []byte("Aliens"), 1)
	_, err = jsonlines.Verify(edited)
	require.Error(t, err)
	resigned, err := signDocument(edited, key)
	require.NoError(t, err)
	info2, err := jsonlines.Verify(resigned)
	require.NoError(t, err)
	require.NotEqual(t, info.Hash, info2.Hash)
}
