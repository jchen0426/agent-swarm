"""Count releases in the last 90 days: PyPI JSON on stdin, or Go proxy .info lines with --go."""
import datetime
import json
import sys

cut = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(days=90)
if "--go" in sys.argv:
    times = [json.loads(line)["Time"] for line in sys.stdin if line.strip()]
else:
    releases = json.load(sys.stdin)["releases"]
    times = [files[0]["upload_time_iso_8601"] for files in releases.values() if files]
recent = [t for t in times if datetime.datetime.fromisoformat(t.replace("Z", "+00:00")) > cut]
print(len(recent))
