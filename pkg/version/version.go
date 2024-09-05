package version

import "os"
import "strings"

var (
	Program = func() string {
		Program := strings.SplitAfterN(os.Args[0], "/", -1)
		return Program[len(Program)-1]
	}()
	ProgramUpper = strings.ToUpper(Program)
	Version      = "dev"
	GitCommit    = "HEAD"

	UpstreamGolang = ""
)
