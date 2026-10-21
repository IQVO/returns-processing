package postgres

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// IDGen is the production ports.IDGen: prefixed, sortable, unique ids
// (`{prefix}-{uuid}`), safe across processes and restarts.
type IDGen struct{}

// NewIDGen returns the production id generator.
func NewIDGen() *IDGen { return &IDGen{} }

// New implements ports.IDGen.
func (IDGen) New(prefix string) string {
	return fmt.Sprintf("%s-%s", strings.ToLower(prefix), uuid.NewString())
}

// Clock is the production ports.Clock: the wall clock, UTC.
type Clock struct{}

// NewClock returns the production clock.
func NewClock() *Clock { return &Clock{} }

// Now implements ports.Clock.
func (Clock) Now() time.Time { return time.Now().UTC() }
