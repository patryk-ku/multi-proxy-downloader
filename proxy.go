// Most of this file was written by generative AI.
package main

import (
	"errors"
	"math/rand"
	"sync"

	"github.com/charmbracelet/log"
)

// ProxyPool manages a rotating pool of proxy addresses assigned to workers.
type ProxyPool struct {
	mu         sync.Mutex
	queue      []string          // available proxies in FIFO order
	assigned   map[string]string // workerID -> proxy
	errorCount int
}

// NewProxyPool initializes a new pool with the given list of proxies.
func NewProxyPool(proxies []string) *ProxyPool {
	queue := make([]string, len(proxies))
	copy(queue, proxies)

	// Randomize queue order
	rand.Shuffle(len(queue), func(i, j int) {
		queue[i], queue[j] = queue[j], queue[i]
	})

	return &ProxyPool{
		queue:      queue,
		assigned:   make(map[string]string),
		errorCount: 0,
	}
}

// Assign returns the proxy assigned to the given workerID.
// If the worker has no proxy yet, assigns the next available one.
// Returns an error if no proxies are available.
func (p *ProxyPool) Assign(workerID string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// If already assigned, return the same proxy
	if proxy, ok := p.assigned[workerID]; ok {
		return proxy, nil
	}

	// Need to assign a new proxy
	if len(p.queue) == 0 {
		return "", errors.New("no proxies available")
	}
	// Pop from head of queue
	proxy := p.queue[0]
	p.queue = p.queue[1:]

	// Record assignment
	p.assigned[workerID] = proxy
	if verbose && debugProxy {
		log.Debug("Proxy assigned to worker.", "worker id", workerID, "adress", proxy)
	}
	return proxy, nil
}

// Fail reports that the worker's proxy has failed.
// It unassigns the proxy, requeues it at the end, and assigns a new one.
func (p *ProxyPool) Fail(workerID string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Check existing assignment
	proxy, ok := p.assigned[workerID]
	if !ok {
		// No proxy to fail; simply assign new
		return p.assignLocked(workerID)
	}

	p.errorCount++

	// Remove assignment
	delete(p.assigned, workerID)

	// Requeue failed proxy at end
	p.queue = append(p.queue, proxy)

	// Assign next proxy
	return p.assignLocked(workerID)
}

// assignLocked assigns a proxy to workerID. Caller must hold lock.
func (p *ProxyPool) assignLocked(workerID string) (string, error) {
	if len(p.queue) == 0 {
		return "", errors.New("no proxies available")
	}
	proxy := p.queue[0]
	p.queue = p.queue[1:]
	p.assigned[workerID] = proxy
	if verbose && debugProxy {
		log.Debug("New proxy assigned to worker.", "worker id", workerID, "adress", proxy)
	}
	return proxy, nil
}

// Release frees the proxy assigned to a worker without requeueing.
// Use this if a worker finishes normally.
func (p *ProxyPool) Release(workerID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	proxy, ok := p.assigned[workerID]
	if !ok {
		return errors.New("no proxy assigned to worker")
	}
	// Remove assignment
	delete(p.assigned, workerID)

	// Return back to the start of the queue
	p.queue = append([]string{proxy}, p.queue...)
	return nil
}
