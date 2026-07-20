import urllib.request
import urllib.parse
import json
import uuid

VIDEOS = ["jNQXAC9IVRw", "kJQP7kiw5Fk", "L_jWHffIx5E"] # Me at zoo is ancient, let's use recent ones
VIDEOS = ["kJQP7kiw5Fk", "fRh_vgS2dFE", "dQw4w9WgXcQ"] # Despacito, Justin Bieber Sorry, Rick Roll

CLIENTS = [
    {"clientName": "WEB", "clientVersion": "2.20260114.01.00", "userAgent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"},
    {"clientName": "WEB_EMBEDDED_PLAYER", "clientVersion": "1.20260114.01.00", "userAgent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"},
    {"clientName": "WEB_CREATOR", "clientVersion": "1.20260114.01.00", "userAgent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"},
    {"clientName": "WEB_REMIX", "clientVersion": "1.20260114.01.00", "userAgent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"},
    {"clientName": "MWEB", "clientVersion": "2.20260115.01.00", "userAgent": "Mozilla/5.0 (iPad; CPU OS 16_7_10 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.6 Mobile/15E148 Safari/604.1"},
    {"clientName": "TVHTML5", "clientVersion": "7.20260114.12.00", "userAgent": "Mozilla/5.0 (ChromiumStylePlatform) Cobalt/25.lts.30.1034943-gold (unlike Gecko), Unknown_TV_Unknown_0/Unknown (Unknown, Unknown)"},
    {"clientName": "TVHTML5_SIMPLY_EMBEDDED_PLAYER", "clientVersion": "2.0", "userAgent": "Mozilla/5.0 (ChromiumStylePlatform) Cobalt/25.lts.30.1034943-gold (unlike Gecko), Unknown_TV_Unknown_0/Unknown (Unknown, Unknown)"},
    {"clientName": "IOS", "clientVersion": "21.02.3", "userAgent": "com.google.ios.youtube/21.02.3 (iPhone16,2; U; CPU iOS 18_3_2 like Mac OS X;)"},
    {"clientName": "ANDROID", "clientVersion": "19.30.36", "userAgent": "com.google.android.youtube/19.30.36 (Linux; U; Android 14; en_US; Pixel 8 Pro Build/AP2A.240705.005)"},
    {"clientName": "ANDROID_VR", "clientVersion": "1.71.26", "userAgent": "com.google.android.apps.youtube.vr.oculus/1.71.26 (Linux; U; Android 12L; eureka-user Build/SQ3A.220605.009.A1) gzip"},
    {"clientName": "ANDROID_TESTSUITE", "clientVersion": "1.9", "userAgent": "com.google.android.youtube/1.9"}
]

def fetch_player(video_id, client):
    url = "https://www.youtube.com/youtubei/v1/player"
    headers = {
        "Content-Type": "application/json",
        "User-Agent": client.get("userAgent", "Mozilla/5.0")
    }
    
    payload = {
        "videoId": video_id,
        "context": {
            "client": {
                "clientName": client["clientName"],
                "clientVersion": client["clientVersion"],
                "visitorData": "CgtmZEZfT2ExT0E0dyit1Ie8BjIKCgJVUxIEGgAgFQ%3D%3D"
            }
        }
    }
    
    req = urllib.request.Request(url, data=json.dumps(payload).encode('utf-8'), headers=headers)
    try:
        resp = urllib.request.urlopen(req)
        return json.loads(resp.read().decode('utf-8'))
    except Exception as e:
        return {"error": str(e)}

def do_ranged_get(url):
    req = urllib.request.Request(url, headers={"Range": "bytes=0-131071"})
    try:
        resp = urllib.request.urlopen(req)
        status = resp.getcode()
        bytes_read = len(resp.read())
        return status, bytes_read
    except Exception as e:
        return str(e), 0

for client in CLIENTS:
    for vid in VIDEOS:
        data = fetch_player(vid, client)
        if "error" in data:
            print(f"[{client['clientName']}] {vid} ERROR {data['error']}")
            continue
        status = data.get("playabilityStatus", {}).get("status", "UNKNOWN")
        streaming = data.get("streamingData", {})
        formats = streaming.get("formats", []) + streaming.get("adaptiveFormats", [])
        
        has_url = False
        url_type = "NONE"
        ranged_status = "N/A"
        ranged_bytes = 0
        n_transform = "N/A"
        
        if formats and "url" in formats[0]:
            has_url = True
            url_type = "PROGRESSIVE" if "formats" in streaming and len(streaming["formats"]) > 0 else "ADAPTIVE"
            target_url = formats[0]["url"]
            if "n=" in target_url:
                n_transform = "YES"
            else:
                n_transform = "NO"
            ranged_status, ranged_bytes = do_ranged_get(target_url)
        elif "serverAbrStreamingUrl" in streaming:
            url_type = "SABR"
            
        print(f"[{client['clientName']}] {vid} | Status: {status} | URL: {url_type} | RangeGET: {ranged_status} ({ranged_bytes} bytes) | N-Trans: {n_transform}")

