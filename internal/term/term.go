// Package term switches a terminal into raw mode for the line editor and the
// session picker, and reports its width. Raw mode is held only while reading
// keys, so Ctrl+C during a turn keeps its normal signal behavior.
package term
