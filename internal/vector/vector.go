// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Atviro kodo sprendimai

// Package vector is the persistent vector collection, behind which chromem-go sits. It keeps vectors only: the text is in SQLite.
package vector

import (
	"context"
	"errors"
	"strconv"

	chromem "github.com/philippgille/chromem-go"
)

// Index is one collection.
type Index struct {
	c *chromem.Collection
}

// Hit is one nearest neighbour.
type Hit struct {
	ID    int64
	Score float32
}

// noEmbed refuses: vectors come from TEI, so chromem must never try its default remote embedder.
func noEmbed(context.Context, string) ([]float32, error) {
	return nil, errors.New("vector: documents carry their own embeddings")
}

// Open loads the collection in dir; chromem reads every document into memory here.
func Open(dir string) (*Index, error) {
	db, err := chromem.NewPersistentDB(dir, false)
	if err != nil {
		return nil, err
	}
	c, err := db.GetOrCreateCollection("exchanges", nil, noEmbed)
	if err != nil {
		return nil, err
	}
	return &Index{c: c}, nil
}

// Add stores or replaces one vector. The only content is the id: chromem refuses neither, and no text is duplicated on disk.
func (x *Index) Add(ctx context.Context, id int64, v []float32) error {
	return x.c.AddDocument(ctx, chromem.Document{ID: strconv.FormatInt(id, 10), Embedding: v})
}

// Count is the number of vectors.
func (x *Index) Count() int { return x.c.Count() }

// Delete removes the vectors of these ids; an id without one is not an error.
func (x *Index) Delete(ctx context.Context, ids ...int64) error {
	if len(ids) == 0 {
		return nil
	}
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.FormatInt(id, 10)
	}
	return x.c.Delete(ctx, nil, nil, s...)
}

// Query returns the n nearest; fewer when the collection is smaller.
func (x *Index) Query(ctx context.Context, v []float32, n int) ([]Hit, error) {
	n = min(n, x.c.Count())
	if n == 0 {
		return nil, nil
	}
	res, err := x.c.QueryEmbedding(ctx, v, n, nil, nil)
	if err != nil {
		return nil, err
	}
	out := make([]Hit, 0, len(res))
	for _, r := range res {
		id, err := strconv.ParseInt(r.ID, 10, 64)
		if err != nil {
			return nil, err
		}
		out = append(out, Hit{ID: id, Score: r.Similarity})
	}
	return out, nil
}
