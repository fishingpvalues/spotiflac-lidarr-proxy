package sabnzbd

import "regexp"

// semverShaped matches exactly what Lidarr's SABnzbd client is willing to
// parse: three dot-separated numbers, nothing else.
var semverShaped = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// developVersion is the one non-numeric string Lidarr special-cases. It reads
// it as "SABnzbd 3.0.0 or newer" and proceeds.
const developVersion = "develop"

// sabnzbdVersion maps this build's version onto something Lidarr accepts.
//
// Lidarr's Sabnzbd.TestConnection parses mode=version and fails the whole
// download client on anything that is neither strict X.Y.Z nor the literal
// "develop":
//
//	Unknown Version: beta-65825a5
//
// That is a hard error (HTTP 400 on the Test button, and a persistent health
// warning), so every image tag that is not a release - :beta, :latest, a
// local build, a git describe - made the download client untestable even
// though it worked perfectly. Release builds were fine, which is exactly why
// it went unnoticed.
//
// The real build string stays visible: /health reports it, and so does
// mode=server_stats. Only the SABnzbd version handshake is normalized.
func sabnzbdVersion(build string) string {
	if semverShaped.MatchString(build) {
		return build
	}
	return developVersion
}
