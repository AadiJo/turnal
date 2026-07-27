package buildinfo

import "github.com/AadiJo/turnal/internal/upgrade"

var Version = "0.0.0"
var Channel = upgrade.ChannelDev
var Commit = ""
var InstallSource = upgrade.InstallSourceUnknown

func Current() upgrade.Metadata {
	return upgrade.Metadata{
		Version:       Version,
		Channel:       Channel,
		Commit:        Commit,
		InstallSource: InstallSource,
	}.Normalize()
}
