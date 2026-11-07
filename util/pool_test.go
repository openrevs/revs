package util

import (
	"io/ioutil"
	"math/rand"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/dchest/uniuri"
	"github.com/stretchr/testify/assert"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

func TestReadWrite(t *testing.T) {
	root, err := ioutil.TempDir("", "pooltest")
	assert.NoError(t, err)
	defer os.RemoveAll(root)

	p := NewPool(root)

	filename, data := randomFile()
	err = p.Write(filename, data)
	assert.NoError(t, err)

	id, rev, _ := splitID(filename)

	dataNew, err := p.Read(id, rev)
	assert.NoError(t, err)
	assert.Equal(t, data, dataNew)

	_, err = p.Read(id, rev+1)
	assert.Error(t, err)
}

func TestSplitID(t *testing.T) {
	id, rev, err := splitID("foobar_95")

	assert.NoError(t, err)
	assert.Equal(t, id, "foobar")
	assert.Equal(t, rev, 95)

	id, rev, err = splitID("foobar")
	assert.Error(t, err)

	id, rev, err = splitID("foobar_95_abc")
	assert.Error(t, err)

	id, rev, err = splitID("foobar_abc")
	assert.Error(t, err)

	id, rev, err = splitID("foobar_-1")
	assert.Error(t, err)
}

func TestRandomFile(t *testing.T) {
	filename, data := randomFile()
	assert.GreaterOrEqual(t, len(data), 50)

	_, _, err := splitID(filename)
	assert.NoError(t, err)
}

func randomFile() (string, []byte) {
	id := uniuri.New()
	rev := rand.Intn(100)

	filename := id + "_" + strconv.Itoa(rev)

	data := make([]byte, 50+rand.Intn(1000))
	rand.Read(data)

	return filename, data
}
