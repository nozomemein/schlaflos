// Package launchd embeds the LaunchDaemon plist template.
//
// StartInterval makes launchd run the reconciler at the configured poll
// interval; RunAtLoad runs it at boot and immediately after installation.
// launchd also starts an interval job once after the machine wakes when
// intervals were missed during sleep, which is how "reconcile immediately
// after any wake" is achieved without a resident daemon.
package launchd

import _ "embed"

// PlistTemplate is the text/template source of the LaunchDaemon plist.
//
//go:embed io.github.nozomemein.schlaflos.plist.tmpl
var PlistTemplate string
