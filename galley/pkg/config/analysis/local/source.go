// Copyright 2019 Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package local

import (
	"fmt"
	"sync"

	"istio.io/istio/galley/pkg/config/event"
	"istio.io/istio/galley/pkg/config/meta/schema/collection"
)

// precedenceSource is a processor.Source implementation that combines multiple sources in precedence order
// Such that events from sources later in the input list take precedence over events affecting
// the same resource from sources earlier in the list
// Only events from the highest precedence source so far are allowed through.
type precedenceSource struct {
	mu      sync.Mutex
	started bool

	inputs  []event.Source
	handler event.Handler

	eventStateMu     sync.Mutex
	resourcePriority map[string]int
	fullSyncCounts   map[collection.Name]int
}

type precedenceHandler struct {
	precedence int
	src        *precedenceSource
}

var _ event.Source = &precedenceSource{}

func newPrecedenceSource(sources []event.Source) *precedenceSource {
	return &precedenceSource{
		inputs:           sources,
		resourcePriority: make(map[string]int),
		fullSyncCounts:   make(map[collection.Name]int),
	}
}

// Handle implements event.Handler
func (ph *precedenceHandler) Handle(e event.Event) {
	ph.src.eventStateMu.Lock()
	defer ph.src.eventStateMu.Unlock()

	switch e.Kind {
	case event.Added, event.Updated, event.Deleted:
		ph.handleEvent(e)
	case event.FullSync:
		ph.handleFullSync(e)
	default:
		ph.src.handler.Handle(e)
	}
}

// handleFullSync handles FullSync events, which are a special case.
// For each collection, we want to only send this once, after all upstream sources have sent theirs.
func (ph *precedenceHandler) handleFullSync(e event.Event) {
	ph.src.fullSyncCounts[e.Source]++
	if ph.src.fullSyncCounts[e.Source] != len(ph.src.inputs) {
		return
	}
	ph.src.handler.Handle(e)
}

// handleEvent handles non fullsync events.
// For each event, only pass it along to the downstream handler if the source it came from
// had equal or higher precedence on the current resource
func (ph *precedenceHandler) handleEvent(e event.Event) {
	key := fmt.Sprintf("%s/%s", e.Source, e.Entry.Metadata.Name)
	curPrecedence, ok := ph.src.resourcePriority[key]
	if ok && ph.precedence < curPrecedence {
		return
	}
	ph.src.resourcePriority[key] = ph.precedence
	ph.src.handler.Handle(e)
}

// Dispatch implements event.Source
func (s *precedenceSource) Dispatch(h event.Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.handler = h

	// Inject a precedenceHandler for each source
	// precedence is based on index position (higher index, higher precedence)
	for i, input := range s.inputs {
		ph := &precedenceHandler{
			precedence: i,
			src:        s,
		}
		input.Dispatch(ph)
	}
}

// Start implements processor.Source
func (s *precedenceSource) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return
	}

	for _, i := range s.inputs {
		i.Start()
	}

	s.started = true
}

// Stop implements processor.Source
func (s *precedenceSource) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started {
		return
	}

	s.started = false

	for _, i := range s.inputs {
		i.Stop()
	}
}
