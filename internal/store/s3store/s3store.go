// Package s3store is the S3 store provider (ADR-004), for the hosted service and the CI-native
// host, where state must outlive any one process. Compare-and-swap is S3's conditional write:
// If-None-Match: * to create, If-Match: <etag> to replace only the version that was read.
//
// Layout under the prefix:
//
//	docs/<key>                 documents; the ETag is the version
//	log/<item>/<ulid>.json     the item's log, one object per entry, created once
//	due/<millis>/<item>        timer markers, listed in time order by prefix
//	timer/<item>               which marker is current; a stale marker is skipped and deleted
package s3store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/eyelock/ynf/internal/store"
)

// Store is an S3-backed store.Store.
type Store struct {
	c      *s3.Client
	bucket string
	prefix string
}

// Open opens s3://bucket/prefix[?region=..&endpoint=..&path_style=true]. Credentials come from the
// default AWS chain (AWS_PROFILE and friends).
func Open(ctx context.Context, rawURL string) (*Store, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "s3" || u.Host == "" {
		return nil, fmt.Errorf("store %q: want s3://bucket/prefix", rawURL)
	}
	q := u.Query()
	var opts []func(*config.LoadOptions) error
	if r := q.Get("region"); r != "" {
		opts = append(opts, config.WithRegion(r))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	c := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if ep := q.Get("endpoint"); ep != "" {
			o.BaseEndpoint = aws.String(ep)
		}
		o.UsePathStyle = q.Get("path_style") == "true"
	})
	return New(c, u.Host, strings.Trim(u.Path, "/")), nil
}

// New wraps a client.
func New(c *s3.Client, bucket, prefix string) *Store {
	if prefix != "" {
		prefix += "/"
	}
	return &Store{c: c, bucket: bucket, prefix: prefix}
}

func (s *Store) key(parts ...string) string { return s.prefix + strings.Join(parts, "/") }

// conflict reports whether an S3 error means a condition failed.
func conflict(err error) bool {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "PreconditionFailed", "ConditionalRequestConflict":
			return true
		}
	}
	return false
}

func notFound(err error) bool {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return true
		}
	}
	return false
}

// Get implements store.Store.
func (s *Store) Get(ctx context.Context, key string) ([]byte, string, error) {
	out, err := s.c.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: aws.String(s.key("docs", key))})
	if notFound(err) {
		return nil, "", store.ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = out.Body.Close() }()
	b, err := io.ReadAll(out.Body)
	return b, aws.ToString(out.ETag), err
}

// Put implements store.Store.
func (s *Store) Put(ctx context.Context, key string, doc []byte, ifVersion string) (string, error) {
	in := &s3.PutObjectInput{Bucket: &s.bucket, Key: aws.String(s.key("docs", key)), Body: bytes.NewReader(doc), ContentType: aws.String("application/json")}
	if ifVersion == "" {
		in.IfNoneMatch = aws.String("*")
	} else {
		in.IfMatch = aws.String(ifVersion)
	}
	out, err := s.c.PutObject(ctx, in)
	if conflict(err) || (ifVersion != "" && notFound(err)) {
		return "", store.ErrConflict
	}
	if err != nil {
		return "", err
	}
	return aws.ToString(out.ETag), nil
}

func (s *Store) list(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	p := s3.NewListObjectsV2Paginator(s.c, &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
	}
	return keys, nil
}

// Keys implements store.Store.
func (s *Store) Keys(ctx context.Context, prefix string) ([]string, error) {
	base := s.key("docs", "")
	keys, err := s.list(ctx, base+prefix)
	if err != nil {
		return nil, err
	}
	for i, k := range keys {
		keys[i] = strings.TrimPrefix(k, base)
	}
	slices.Sort(keys)
	return keys, nil
}

type logObject struct {
	Time time.Time       `json:"time"`
	Kind string          `json:"kind"`
	Body json.RawMessage `json:"body"`
}

// Append implements store.Store.
func (s *Store) Append(ctx context.Context, item string, e store.LogEntry) error {
	b, err := json.Marshal(logObject{Time: e.Time.UTC(), Kind: e.Kind, Body: e.Body})
	if err != nil {
		return err
	}
	_, err = s.c.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: aws.String(s.key("log", item, e.ID+".json")),
		Body: bytes.NewReader(b), IfNoneMatch: aws.String("*"), ContentType: aws.String("application/json")})
	if conflict(err) {
		return store.ErrConflict
	}
	return err
}

// Log implements store.Store.
func (s *Store) Log(ctx context.Context, item string) ([]store.LogEntry, error) {
	base := s.key("log", item) + "/"
	keys, err := s.list(ctx, base)
	if err != nil {
		return nil, err
	}
	slices.Sort(keys)
	out := make([]store.LogEntry, 0, len(keys))
	for _, k := range keys {
		id := strings.TrimSuffix(strings.TrimPrefix(k, base), ".json")
		if strings.Contains(id, "/") {
			continue // another item's log nested under this one's name
		}
		obj, err := s.c.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: aws.String(k)})
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(obj.Body)
		_ = obj.Body.Close()
		if err != nil {
			return nil, err
		}
		var lo logObject
		if err := json.Unmarshal(b, &lo); err != nil {
			return nil, fmt.Errorf("log %s: %w", k, err)
		}
		out = append(out, store.LogEntry{ID: id, Time: lo.Time, Kind: lo.Kind, Body: lo.Body})
	}
	return out, nil
}

func marker(item string, at time.Time) string {
	return fmt.Sprintf("due/%013d/%s", at.UnixMilli(), item)
}

// Schedule implements store.Store. The marker is written before the pointer, so a crash between
// leaves an extra marker, which Due skips, never a missing one.
func (s *Store) Schedule(ctx context.Context, item string, at time.Time) error {
	ptr := s.key("timer", item)
	var old string
	if out, err := s.c.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &ptr}); err == nil {
		b, _ := io.ReadAll(out.Body)
		_ = out.Body.Close()
		old = string(b)
	} else if !notFound(err) {
		return err
	}
	if at.IsZero() {
		if _, err := s.c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &ptr}); err != nil {
			return err
		}
	} else {
		m := marker(item, at)
		if _, err := s.c.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: aws.String(s.key(m)), Body: bytes.NewReader(nil)}); err != nil {
			return err
		}
		if _, err := s.c.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: &ptr, Body: strings.NewReader(m)}); err != nil {
			return err
		}
		if old == m {
			return nil
		}
	}
	if old != "" {
		_, _ = s.c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: aws.String(s.key(old))})
	}
	return nil
}

// Due implements store.Store: markers are listed in time order, and one that is no longer the
// item's current timer is deleted and skipped.
func (s *Store) Due(ctx context.Context, now time.Time, limit int) ([]string, error) {
	base := s.key("due") + "/"
	keys, err := s.list(ctx, base)
	if err != nil {
		return nil, err
	}
	slices.Sort(keys)
	var out []string
	for _, k := range keys {
		rest := strings.TrimPrefix(k, base)
		ts, item, ok := strings.Cut(rest, "/")
		ms, err := strconv.ParseInt(ts, 10, 64)
		if !ok || err != nil {
			continue
		}
		if ms > now.UnixMilli() || len(out) >= limit {
			break
		}
		ptr, err := s.c.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: aws.String(s.key("timer", item))})
		current := ""
		if err == nil {
			b, _ := io.ReadAll(ptr.Body)
			_ = ptr.Body.Close()
			current = string(b)
		} else if !notFound(err) {
			return nil, err
		}
		if current != "due/"+rest {
			_, _ = s.c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: aws.String(k)})
			continue
		}
		if !slices.Contains(out, item) {
			out = append(out, item)
		}
	}
	return out, nil
}

// Close implements store.Store.
func (s *Store) Close() error { return nil }
