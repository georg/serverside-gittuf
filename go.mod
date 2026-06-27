module github.com/georg/serverside-gittuf

go 1.26.4

require (
	github.com/go-git/go-billy/v6 v6.0.0-alpha.1.0.20260519112248-0095b064a6c6
	github.com/go-git/go-git/v6 v6.0.0-alpha.4.0.20260626131229-c31b1f53b87e
	github.com/go-git/x/plugin/objectsigner/ssh v0.2.0
	github.com/go-git/x/plugin/objectverifier/ssh v0.0.0-20260626134819-111a9411a033
	github.com/stretchr/testify v1.11.1
	golang.org/x/crypto v0.53.0
)

// Use the local go-git-storer branch of gittuf, which exposes pkg/rsl behind
// the RSLStorer interface (see ../gittuf).
replace github.com/gittuf/gittuf => github.com/pjbgf/gittuf v0.0.0-20260624132056-1124ad3a423e

// Based on Verifier interface from https://github.com/go-git/go-git/pull/2235
replace github.com/go-git/go-git/v6 => github.com/go-git/go-git/v6 v6.0.0-alpha.4.0.20260626131229-c31b1f53b87e

// Based on Verified implementation from https://github.com/go-git/x/pull/13
replace github.com/go-git/x/plugin/objectverifier/ssh => github.com/go-git/x/plugin/objectverifier/ssh v0.0.0-20260626134819-111a9411a033

require (
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/emirpasic/gods v1.18.1 // indirect
	github.com/go-git/gcfg/v2 v2.0.2 // indirect
	github.com/hiddeco/sshsig v0.2.0 // indirect
	github.com/kevinburke/ssh_config v1.6.0 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/pjbgf/sha1cd v0.6.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/sergi/go-diff v1.4.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
