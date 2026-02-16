// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"sort"
	"sync"
	"time"

	"k8s.io/client-go/rest"
)

// TargetInfo describes a registered target cluster.
// It can be backed by a local Kubernetes Secret (SecretName/Namespace set)
// or by an in-memory rest.Config discovered from a hub (RestConfig set).
type TargetInfo struct {
	SecretName string    `json:"secret_name,omitempty"`
	Namespace  string    `json:"namespace,omitempty"`
	Key        string    `json:"key"`
	Source     string    `json:"source"` // "secret" or "discovered:<hub-key>"
	AddedAt    time.Time `json:"added_at"`
	// RestConfig holds an in-memory config for discovered managed clusters.
	// Not serialized to JSON.
	RestConfig *rest.Config `json:"-"`
}

// TargetStore is a thread-safe in-memory store for registered target cluster references.
type TargetStore struct {
	mu      sync.RWMutex
	targets map[string]TargetInfo // key: "secretName/namespace"
}

// NewTargetStore creates a new empty TargetStore.
func NewTargetStore() *TargetStore {
	return &TargetStore{
		targets: make(map[string]TargetInfo),
	}
}

// targetKey returns the canonical key for a target: "secretName/namespace".
func targetKey(secretName, namespace string) string {
	return secretName + "/" + namespace
}

// Add registers a secret-backed target cluster in the store. Returns the key.
func (s *TargetStore) Add(secretName, namespace string) string {
	key := targetKey(secretName, namespace)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets[key] = TargetInfo{
		SecretName: secretName,
		Namespace:  namespace,
		Key:        key,
		Source:     "secret",
		AddedAt:    time.Now(),
	}
	return key
}

// AddWithConfig registers a target cluster backed by an in-memory rest.Config
// (e.g. discovered from an ACM hub). The key is typically the cluster name.
func (s *TargetStore) AddWithConfig(key string, config *rest.Config, source string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets[key] = TargetInfo{
		Key:        key,
		Source:     source,
		RestConfig: config,
		AddedAt:    time.Now(),
	}
	return key
}

// Remove unregisters a target from the store. Returns true if it existed.
func (s *TargetStore) Remove(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, existed := s.targets[key]
	delete(s.targets, key)
	return existed
}

// List returns all registered targets, sorted by key.
func (s *TargetStore) List() []TargetInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]TargetInfo, 0, len(s.targets))
	for _, t := range s.targets {
		result = append(result, t)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Key < result[j].Key
	})
	return result
}

// Get looks up a target by its key ("secretName/namespace").
func (s *TargetStore) Get(key string) (TargetInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.targets[key]
	return t, ok
}

// IsTargetRef returns true if the input string matches a registered target key.
func (s *TargetStore) IsTargetRef(input string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.targets[input]
	return ok
}

// Package-level default target store.
var defaultTargetStore = NewTargetStore()
