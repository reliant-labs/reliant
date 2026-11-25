// Package requireprivatesink declares an interface whose one method shares
// its name, not its signature, with io.Writer's.
package requireprivatesink

// LineSink takes whole lines.
type LineSink interface {
	Write(line string) error
}
