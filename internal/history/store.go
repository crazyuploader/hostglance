// Package history provides bbolt-backed storage for system and storage metrics.
package history

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bucketSeries = []byte("series")

// Point is one time-series sample.
type Point struct {
	Ts    int64   `json:"ts"` // Unix seconds
	Value float64 `json:"v"`
}

// Sample is one value to be written.
type Sample struct {
	Key   string
	Ts    time.Time
	Value float64
}

// SeriesInfo describes one stored series.
type SeriesInfo struct {
	Key    string `json:"key"`
	Node   string `json:"node"`
	Kind   string `json:"kind"`   // "pool", "disk", or "system"
	Name   string `json:"name"`   // pool name, device path, or "node"
	Metric string `json:"metric"` // e.g. "used_pct", "temp_c", "cpu_pct"
}

// Store is a bbolt-backed time-series store.
type Store struct {
	db        *bolt.DB
	retention time.Duration
}

// Open opens or creates the bbolt database at path.
// The parent directory is created automatically if it does not exist.
func Open(path string, retention time.Duration) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return nil, err
		}
	}
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucketSeries)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, retention: retention}, nil
}

// Retention returns the configured retention duration.
func (s *Store) Retention() time.Duration {
	return s.retention
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// SeriesKey builds a canonical series key from its components.
// Uses unit separator (0x1f) to avoid collisions with user data.
func SeriesKey(node, kind, name, metric string) string {
	return node + "\x1f" + kind + "\x1f" + name + "\x1f" + metric
}

// WriteBatch writes multiple samples in a single transaction.
func (s *Store) WriteBatch(samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		parent := tx.Bucket(bucketSeries)
		// Cache bucket handles within the transaction to avoid redundant lookups
		// when multiple samples share the same series key.
		buckets := make(map[string]*bolt.Bucket, len(samples))
		for _, sp := range samples {
			if sp.Ts.Unix() < 0 {
				continue // skip invalid timestamps
			}
			b, ok := buckets[sp.Key]
			if !ok {
				var err error
				b, err = parent.CreateBucketIfNotExists([]byte(sp.Key))
				if err != nil {
					return err
				}
				buckets[sp.Key] = b
			}
			var k [4]byte
			binary.BigEndian.PutUint32(k[:], uint32(sp.Ts.Unix()))
			var v [4]byte
			binary.BigEndian.PutUint32(v[:], math.Float32bits(float32(sp.Value)))
			if err := b.Put(k[:], v[:]); err != nil {
				return err
			}
		}
		return nil
	})
}

// Query returns samples for key in [from, to].
// If bucketSecs > 0, time-bucketed averages are returned instead of raw points.
func (s *Store) Query(key string, from, to time.Time, bucketSecs int64) ([]Point, error) {
	var points []Point
	err := s.db.View(func(tx *bolt.Tx) error {
		parent := tx.Bucket(bucketSeries)
		b := parent.Bucket([]byte(key))
		if b == nil {
			return nil
		}

		var fromKey [4]byte
		var toKey [4]byte
		binary.BigEndian.PutUint32(fromKey[:], uint32(from.Unix()))
		binary.BigEndian.PutUint32(toKey[:], uint32(to.Unix()))

		c := b.Cursor()

		if bucketSecs <= 0 {
			for k, v := c.Seek(fromKey[:]); k != nil; k, v = c.Next() {
				if bytes.Compare(k, toKey[:]) > 0 {
					break
				}
				ts := int64(binary.BigEndian.Uint32(k))
				val := float64(math.Float32frombits(binary.BigEndian.Uint32(v)))
				points = append(points, Point{Ts: ts, Value: val})
			}
			return nil
		}

		// Bucketed averages
		var (
			bucketStart int64 = -1
			sum         float64
			count       int
		)
		flush := func() {
			if count > 0 {
				points = append(points, Point{Ts: bucketStart, Value: sum / float64(count)})
			}
		}
		for k, v := c.Seek(fromKey[:]); k != nil; k, v = c.Next() {
			if bytes.Compare(k, toKey[:]) > 0 {
				break
			}
			ts := int64(binary.BigEndian.Uint32(k))
			val := float64(math.Float32frombits(binary.BigEndian.Uint32(v)))
			bucket := (ts / bucketSecs) * bucketSecs
			if bucket != bucketStart {
				flush()
				bucketStart = bucket
				sum = 0
				count = 0
			}
			sum += val
			count++
		}
		flush()
		return nil
	})
	return points, err
}

// Prune deletes samples older than the retention period.
// No-op when retention <= 0.
// Uses a read transaction to collect series names, then a short write
// transaction per series to avoid holding the write lock for the full scan.
func (s *Store) Prune() error {
	if s.retention <= 0 {
		return nil
	}
	cutoffTs := uint32(time.Now().Add(-s.retention).Unix())

	// Collect series names under a cheap read lock.
	var seriesNames [][]byte
	if err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSeries).ForEach(func(name, val []byte) error {
			if val != nil {
				return nil // skip non-bucket values
			}
			cp := make([]byte, len(name))
			copy(cp, name)
			seriesNames = append(seriesNames, cp)
			return nil
		})
	}); err != nil {
		return err
	}

	// Delete stale keys one series at a time to keep write transactions short.
	for _, name := range seriesNames {
		if err := s.db.Update(func(tx *bolt.Tx) error {
			b := tx.Bucket(bucketSeries).Bucket(name)
			if b == nil {
				return nil
			}
			c := b.Cursor()
			var toDelete [][]byte
			for ck, _ := c.First(); ck != nil; ck, _ = c.Next() {
				if binary.BigEndian.Uint32(ck) >= cutoffTs {
					break
				}
				cp := make([]byte, len(ck))
				copy(cp, ck)
				toDelete = append(toDelete, cp)
			}
			for _, k := range toDelete {
				if err := b.Delete(k); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// DeleteSeries removes a series and all its points entirely. Used to purge
// series that should no longer be recorded (e.g. after a filter change)
// rather than waiting for retention to age them out.
func (s *Store) DeleteSeries(key string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSeries).DeleteBucket([]byte(key))
	})
}

// ListSeries returns metadata for all stored series.
func (s *Store) ListSeries() ([]SeriesInfo, error) {
	var series []SeriesInfo
	err := s.db.View(func(tx *bolt.Tx) error {
		parent := tx.Bucket(bucketSeries)
		return parent.ForEach(func(k, v []byte) error {
			if v != nil {
				return nil // skip non-bucket values
			}
			key := string(k)
			parts := strings.SplitN(key, "\x1f", 4)
			if len(parts) != 4 {
				return nil
			}
			series = append(series, SeriesInfo{
				Key:    key,
				Node:   parts[0],
				Kind:   parts[1],
				Name:   parts[2],
				Metric: parts[3],
			})
			return nil
		})
	})
	return series, err
}
