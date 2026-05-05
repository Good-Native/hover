package lighthouse

import "github.com/good-native/hover/internal/logging"

// lighthouseLog is the package-scoped structured logger. Mirrors the
// brokerLog / dbLog pattern used elsewhere in the codebase: one
// component name per package so log filters compose cleanly across the
// scheduler, runner, and consumer surfaces.
var lighthouseLog = logging.Component("lighthouse")
