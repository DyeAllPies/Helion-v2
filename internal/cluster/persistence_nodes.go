// internal/cluster/persistence_nodes.go
//
// BadgerJSONPersister node methods (satisfies Persister interface).

package cluster

import (
	"context"
	"encoding/json"
	"fmt"

	badger "github.com/dgraph-io/badger/v4"

	cpb "github.com/DyeAllPies/Helion-v2/internal/proto/coordinatorpb"
)

// SaveNode writes a Node record under nodes/{address} with TTL = 2× heartbeat interval.
func (p *BadgerJSONPersister) SaveNode(_ context.Context, n *cpb.Node) error {
	data, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("SaveNode marshal: %w", err)
	}
	key := []byte("nodes/" + n.Address)
	ttl := 2 * p.heartbeatInterval
	return p.db.Update(func(txn *badger.Txn) error {
		return txn.SetEntry(badger.NewEntry(key, data).WithTTL(ttl))
	})
}

// LoadAllNodes reads all nodes/ entries for crash-recovery on startup.
func (p *BadgerJSONPersister) LoadAllNodes(_ context.Context) ([]*cpb.Node, error) {
	var nodes []*cpb.Node
	prefix := []byte("nodes/")
	err := p.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Rewind(); it.Valid(); it.Next() {
			var n cpb.Node
			if err := it.Item().Value(func(v []byte) error {
				return json.Unmarshal(v, &n)
			}); err != nil {
				return fmt.Errorf("LoadAllNodes unmarshal %q: %w", it.Item().Key(), err)
			}
			nodes = append(nodes, &n)
		}
		return nil
	})
	return nodes, err
}
