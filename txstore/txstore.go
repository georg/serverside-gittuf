// Package txstore adapts a go-git v6 storage.Storer that does not implement
// storer.Transactioner (notably the filesystem backend) into one that does, so
// the RSL append can write objects through a storer.Transaction and flush them
// in one durable Commit (the spec's Transactioner variant). A storer that
// already implements Transactioner (e.g. go-git's in-memory storage) is returned
// unwrapped and uses its native transaction.
package txstore

import (
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/storer"
	"github.com/go-git/go-git/v6/storage"
)

// Storer is a go-git storage.Storer that also supports object transactions
// (storer.Transactioner). It is the storer shape the RSL append consumes.
type Storer interface {
	storage.Storer
	Begin() storer.Transaction
}

// New returns s as a transactional Storer: unchanged when it already implements
// Transactioner, otherwise wrapped with an in-memory staging transaction that
// flushes to s on Commit.
func New(s storage.Storer) Storer {
	if ts, ok := s.(Storer); ok {
		return ts
	}
	return &wrapper{Storer: s}
}

type wrapper struct {
	storage.Storer
}

func (w *wrapper) Begin() storer.Transaction {
	return &tx{parent: w.Storer, objects: map[plumbing.Hash]plumbing.EncodedObject{}}
}

// tx stages objects in memory (serving read-your-own-writes) and writes them to
// the parent storer on Commit — the same shape as go-git's in-memory
// TxObjectStorage, but over an arbitrary backend.
type tx struct {
	parent  storage.Storer
	objects map[plumbing.Hash]plumbing.EncodedObject
}

func (t *tx) SetEncodedObject(o plumbing.EncodedObject) (plumbing.Hash, error) {
	h := o.Hash()
	t.objects[h] = o
	return h, nil
}

func (t *tx) EncodedObject(typ plumbing.ObjectType, h plumbing.Hash) (plumbing.EncodedObject, error) {
	if o, ok := t.objects[h]; ok && (typ == plumbing.AnyObject || o.Type() == typ) {
		return o, nil
	}
	return t.parent.EncodedObject(typ, h)
}

func (t *tx) Commit() error {
	for h, o := range t.objects {
		delete(t.objects, h)
		if _, err := t.parent.SetEncodedObject(o); err != nil {
			return err
		}
	}
	return nil
}

func (t *tx) Rollback() error {
	clear(t.objects)
	return nil
}
