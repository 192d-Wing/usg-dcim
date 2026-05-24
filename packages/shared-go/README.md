# shared-go

Small Go utilities shared by 2+ animal packages. **Add code here only
when it has at least two real consumers** — keep the bar high so this
doesn't drift into a junk drawer.

## env

Typed `os.Getenv` wrappers with defaults. Used by `heron`, `magpie`,
`beagle` for configuring their main loops without dragging in a flag/yaml
parser.
