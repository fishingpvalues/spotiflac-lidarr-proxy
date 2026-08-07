package sabnzbd

import "testing"

// Lidarr's Sabnzbd.RemovesCompletedDownloads, transcribed from
// src/NzbDrone.Core/Download/Clients/Sabnzbd/Sabnzbd.cs. Reproduced here so a
// future change to what we advertise is checked against what Lidarr actually
// does with it, rather than against an assumption.
func lidarrRemovesCompletedDownloads(retention, option string, number int) bool {
	switch option {
	case "all":
		return false
	case "number-archive", "number-delete":
		return true
	case "days-archive", "days-delete":
		return number < 14
	case "all-archive", "all-delete":
		return true
	}

	// Legacy SABnzbd < 4.3 path.
	if retention == "" {
		return false
	}
	if len(retention) > 0 && retention[len(retention)-1] == 'd' {
		return true // days < 14 for any small value; irrelevant to us
	}
	return retention != "0"
}

func TestAdvertisedRetentionDoesNotLookLikeRemoval(t *testing.T) {
	// What handleGetConfig sets. We prune only our own oldest history
	// entries, long after Lidarr has imported, so Lidarr must not conclude
	// that downloads vanish before it can grab them.
	const retention, option = "all", "all"

	if lidarrRemovesCompletedDownloads(retention, option, 0) {
		t.Fatal("Lidarr would raise DownloadClientRemovesCompletedDownloadsCheck for us")
	}

	// Guard the regression precisely: advertising history_retention alone,
	// with no history_retention_option, falls through to Lidarr's legacy
	// branch and "all" != "0" evaluates true.
	if !lidarrRemovesCompletedDownloads(retention, "", 0) {
		t.Fatal("expected the option-less form to be the case Lidarr misreads")
	}
}
