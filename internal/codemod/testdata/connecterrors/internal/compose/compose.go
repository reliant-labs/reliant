// Package compose is the composition root's stand-in: it hands a
// *viaiface.Service to lister's Getter. That is what makes lister's read of a
// *connect.Error a read of viaiface's errors. compose reads none itself, so
// importing viaiface keeps nothing wrapped.
package compose

import (
	"example.com/cescratch/internal/handlers/viaiface"
	"example.com/cescratch/internal/lister"
)

// Lister wires the lister to the handler, in process.
func Lister() *lister.Lister { return lister.New(&viaiface.Service{}) }
