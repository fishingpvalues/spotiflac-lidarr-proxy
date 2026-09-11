package sabnzbd

// emulatedVersion is the SABnzbd version this proxy answers mode=version with.
//
// It is deliberately a constant and deliberately not this build's own version.
// Lidarr's Sabnzbd.TestConnection puts three separate constraints on the
// answer, and only a fixed 4.x string satisfies all of them. Measured against
// a real Lidarr 3.1.5.5066:
//
//  1. It must parse. Anything that is neither strict X.Y.Z nor the literal
//     "develop" is a hard error - "Unknown Version: beta-65825a5", HTTP 400 on
//     the Test button plus a persistent health warning. Every image tag that
//     is not a release (:beta, :latest, a local build, a git describe) hit
//     this.
//  2. It must be at least 0.7.0. Reporting this proxy's own version did parse
//     and then failed with "Version 0.7.0+ is required, but found 0.0.3" -
//     the project is pre-1.0, so passing the build string through is a hard
//     error for as long as that is true.
//  3. "develop" clears both of the above and Lidarr special-cases it as "3.0.0
//     or newer", but attaches a warning: "Lidarr may not be able to support
//     new features added to SABnzbd when running develop versions." Lidarr
//     refuses to save a client carrying a warning without forceSave, so adding
//     the download client through the UI came back as an error anyway.
//
// 4.3.3 is what the rest of the emulation already assumes: history_retention
// and history_retention_option, the pair handleGetConfig sets so Lidarr does
// not raise DownloadClientRemovesCompletedDownloadsCheck, are read on SABnzbd
// 4.3 and later.
//
// This proxy's real build string stays visible where it means something:
// /health, mode=server_stats and the Newznab caps document all report it.
const emulatedVersion = "4.3.3"

// sabnzbdVersion is the answer to mode=version. The build string is accepted
// and ignored; see emulatedVersion for why nothing derived from it works.
func sabnzbdVersion(string) string {
	return emulatedVersion
}
