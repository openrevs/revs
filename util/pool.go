package util

import (
	"errors"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"strconv"
	"strings"
)

type Pool struct {
	root string
}

func NewPool(root string) Pool {
	return Pool{
		root: root,
	}
}

func (p Pool) Write(id string, data []byte) error {
	if _, _, err := splitID(id); err != nil {
		return err
	}

	return ioutil.WriteFile(filepath.Join(p.root, id), data, 0644)
}

func (p Pool) Read(id string, rev int) ([]byte, error) {
	filename := id + "_" + strconv.Itoa(rev)

	return ioutil.ReadFile(filepath.Join(p.root, filename))
}

func splitID(fqid string) (string, int, error) {
	s := strings.Split(fqid, "_")
	if len(s) != 2 {
		return "", 0, errors.New("ID must have two parts")
	}

	id := s[0]
	rev, err := strconv.Atoi(s[1])
	switch {
	case err != nil:
		return "", 0, fmt.Errorf("error parsing %s: %w", s[1], err)
	case rev < 0:
		return "", 0, fmt.Errorf("error parsing %s: version cannot be negative", s[1])
	}

	return id, rev, nil
}

/*
type FileStorage struct {
	base string
}

func NewFileStorage(base string) FileStorage {
	return FileStorage{
		base: strings.TrimSpace(base),
	}
}

func (fs FileStorage) Get(key string) ([]byte, error) {
	data, err := ioutil.ReadFile(filepath.Join(fs.base, key))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return data, nil
}

func (fs FileStorage) Delete(key string) error {
	dir, file := filepath.Split(key)
	dir = filepath.Join(fs.base, dir)

	err := os.Remove(filepath.Join(dir, file))
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func (FileStorage) List(prefix string) ([]string, error) {
	panic("implement me")
}

func (fs FileStorage) Put(key string, data []byte) error {
	dir, file := filepath.Split(key)
	dir = filepath.Join(fs.base, dir)

	if fs.base != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	f, err := os.Create(filepath.Join(dir, file))
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return err
	}

	return nil
}

func Load(key string) ([]byte, error) {
	return ioutil.ReadFile(filepath.Join(dataFolder, key))
}

*/
