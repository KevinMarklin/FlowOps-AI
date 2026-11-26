# govulncheck summary

Scanner: v1.10.0; target: local Go 1.25.1; scan date: 2026-11-23.

Static symbol reachability is not a demonstrated exploit. Docker runtime build version is not fixed by the Dockerfile.

## Symbol Results: 48

| ID | Description | Found | Fixed |
|---|---|---|---|
| [GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859) | HTTP/1 server crash due to MaxBytesReader header race in net/http | net/http@go1.25.1 | net/http@go1.26.11 |
| [GO-2026-6809](https://pkg.go.dev/vuln/GO-2026-6809) | Distrust compromised intermediate certificates in crypto/x509 | crypto/x509@go1.25.1 | crypto/x509@go1.26.10 |
| [GO-2026-6629](https://pkg.go.dev/vuln/GO-2026-6629) | Panic parsing crafted input in x/text/secure/precis in golang.org/x/text | golang.org/x/text@v0.18.0 | golang.org/x/text@v0.41.0 |
| [GO-2026-6617](https://pkg.go.dev/vuln/GO-2026-6617) | HTTP/2 server crash due to HPACK encoder race in net/http | net/http@go1.25.1 | net/http@go1.26.9 |
| [GO-2026-6613](https://pkg.go.dev/vuln/GO-2026-6613) | HTTP/1 server connection desynchronization after 2xx CONNECT response in net/http | net/http@go1.25.1 | net/http@go1.26.9 |
| [GO-2026-6612](https://pkg.go.dev/vuln/GO-2026-6612) | Double flow control refund on HTTP/2 server streams in net/http | net/http@go1.25.1 | net/http@go1.26.9 |
| [GO-2026-6611](https://pkg.go.dev/vuln/GO-2026-6611) | Excessive CPU consumption from repeated initial window changes in net/http | net/http@go1.25.1 | net/http@go1.26.9 |
| [GO-2026-6610](https://pkg.go.dev/vuln/GO-2026-6610) | HTTP/2 transport accepts malformed framing-related headers in net/http | net/http@go1.25.1 | net/http@go1.26.9 |
| [GO-2026-6609](https://pkg.go.dev/vuln/GO-2026-6609) | Lack of limit on size of parsed Range headers in net/http | net/http@go1.25.1 | net/http@go1.26.9 |
| [GO-2026-6608](https://pkg.go.dev/vuln/GO-2026-6608) | Memory limit bypass when parsing MIME headers in net/textproto, mime/multipart | net/textproto@go1.25.1 | net/textproto@go1.26.9 |
| [GO-2026-6607](https://pkg.go.dev/vuln/GO-2026-6607) | Reject malformed ECH outer extension references in crypto/tls | crypto/tls@go1.25.1 | crypto/tls@go1.26.9 |
| [GO-2026-6605](https://pkg.go.dev/vuln/GO-2026-6605) | HTTP/1 client connection desynchronization after CONNECT rejection in net/http | net/http@go1.25.1 | net/http@go1.26.9 |
| [GO-2026-6603](https://pkg.go.dev/vuln/GO-2026-6603) | HTTP/2 server memory exhaustion due to Trailer headers in net/http | net/http@go1.25.1 | net/http@go1.26.9 |
| [GO-2026-6218](https://pkg.go.dev/vuln/GO-2026-6218) | Avoid quadratic complexity in resolvePath in net/url | net/url@go1.25.1 | net/url@go1.25.13 |
| [GO-2026-6090](https://pkg.go.dev/vuln/GO-2026-6090) | Limit handshake messages we are willing to accept post-handshake in crypto/tls | crypto/tls@go1.25.1 | crypto/tls@go1.25.13 |
| [GO-2026-6089](https://pkg.go.dev/vuln/GO-2026-6089) | Apply ReadHeaderTimeout when doing unencrypted HTTP/2 check in net/http | net/http@go1.25.1 | net/http@go1.25.13 |
| [GO-2026-6088](https://pkg.go.dev/vuln/GO-2026-6088) | Add recursion depth guard during decode in encoding/xml | encoding/xml@go1.25.1 | encoding/xml@go1.25.13 |
| [GO-2026-5972](https://pkg.go.dev/vuln/GO-2026-5972) | Enforce maximum recursion depth in encoding/asn1 | encoding/asn1@go1.25.1 | encoding/asn1@go1.25.13 |
| [GO-2026-5970](https://pkg.go.dev/vuln/GO-2026-5970) | Infinite loop on invalid input in golang.org/x/text | golang.org/x/text@v0.18.0 | golang.org/x/text@v0.39.0 |
| [GO-2026-5856](https://pkg.go.dev/vuln/GO-2026-5856) | Invoking Encrypted Client Hello privacy leak in crypto/tls | crypto/tls@go1.25.1 | crypto/tls@go1.25.12 |
| [GO-2026-5039](https://pkg.go.dev/vuln/GO-2026-5039) | Arbitrary inputs are included in errors without any escaping in net/textproto | net/textproto@go1.25.1 | net/textproto@go1.25.11 |
| [GO-2026-5038](https://pkg.go.dev/vuln/GO-2026-5038) | Quadratic complexity in WordDecoder.DecodeHeader in mime | mime@go1.25.1 | mime@go1.25.11 |
| [GO-2026-5037](https://pkg.go.dev/vuln/GO-2026-5037) | Inefficient candidate hostname parsing in crypto/x509 | crypto/x509@go1.25.1 | crypto/x509@go1.25.11 |
| [GO-2026-5026](https://pkg.go.dev/vuln/GO-2026-5026) | Invoking failure to reject ASCII-only Punycode-encoded labels in golang.org/x/net/idna | net/http@go1.25.1 | net/http@go1.25.13 |
| [GO-2026-5004](https://pkg.go.dev/vuln/GO-2026-5004) | SQL Injection via placeholder confusion with dollar quoted string literals in github.com/jackc/pgx | github.com/jackc/pgx/v5@v5.7.1 | github.com/jackc/pgx/v5@v5.9.2 |
| [GO-2026-4986](https://pkg.go.dev/vuln/GO-2026-4986) | Quadratic string concatentation in consumeComment in net/mail | net/mail@go1.25.1 | net/mail@go1.25.10 |
| [GO-2026-4977](https://pkg.go.dev/vuln/GO-2026-4977) | Quadratic string concatenation in consumePhrase in net/mail | net/mail@go1.25.1 | net/mail@go1.25.10 |
| [GO-2026-4971](https://pkg.go.dev/vuln/GO-2026-4971) | Panic in Dial and LookupPort when handling NUL byte on Windows in net | net@go1.25.1 | net@go1.25.10 |
| [GO-2026-4947](https://pkg.go.dev/vuln/GO-2026-4947) | Unexpected work during chain building in crypto/x509 | crypto/x509@go1.25.1 | crypto/x509@go1.25.9 |
| [GO-2026-4946](https://pkg.go.dev/vuln/GO-2026-4946) | Inefficient policy validation in crypto/x509 | crypto/x509@go1.25.1 | crypto/x509@go1.25.9 |
| [GO-2026-4918](https://pkg.go.dev/vuln/GO-2026-4918) | Infinite loop in HTTP/2 transport when given bad SETTINGS_MAX_FRAME_SIZE in net/http/internal/http2 in golang.org/x/net | net/http@go1.25.1 | net/http@go1.25.10 |
| [GO-2026-4870](https://pkg.go.dev/vuln/GO-2026-4870) | Unauthenticated TLS 1.3 KeyUpdate record can cause persistent connection retention and DoS in crypto/tls | crypto/tls@go1.25.1 | crypto/tls@go1.25.9 |
| [GO-2026-4602](https://pkg.go.dev/vuln/GO-2026-4602) | FileInfo can escape from a Root in os | os@go1.25.1 | os@go1.25.8 |
| [GO-2026-4601](https://pkg.go.dev/vuln/GO-2026-4601) | Incorrect parsing of IPv6 host literals in net/url | net/url@go1.25.1 | net/url@go1.25.8 |
| [GO-2026-4341](https://pkg.go.dev/vuln/GO-2026-4341) | Memory exhaustion in query parameter parsing in net/url | net/url@go1.25.1 | net/url@go1.25.6 |
| [GO-2026-4340](https://pkg.go.dev/vuln/GO-2026-4340) | Handshake messages may be processed at the incorrect encryption level in crypto/tls | crypto/tls@go1.25.1 | crypto/tls@go1.25.6 |
| [GO-2026-4337](https://pkg.go.dev/vuln/GO-2026-4337) | Unexpected session resumption in crypto/tls | crypto/tls@go1.25.1 | crypto/tls@go1.25.7 |
| [GO-2025-4175](https://pkg.go.dev/vuln/GO-2025-4175) | Improper application of excluded DNS name constraints when verifying wildcard names in crypto/x509 | crypto/x509@go1.25.1 | crypto/x509@go1.25.5 |
| [GO-2025-4155](https://pkg.go.dev/vuln/GO-2025-4155) | Excessive resource consumption when printing error string for host certificate validation in crypto/x509 | crypto/x509@go1.25.1 | crypto/x509@go1.25.5 |
| [GO-2025-4015](https://pkg.go.dev/vuln/GO-2025-4015) | Excessive CPU consumption in Reader.ReadResponse in net/textproto | net/textproto@go1.25.1 | net/textproto@go1.25.2 |
| [GO-2025-4013](https://pkg.go.dev/vuln/GO-2025-4013) | Panic when validating certificates with DSA public keys in crypto/x509 | crypto/x509@go1.25.1 | crypto/x509@go1.25.2 |
| [GO-2025-4012](https://pkg.go.dev/vuln/GO-2025-4012) | Lack of limit when parsing cookies can cause memory exhaustion in net/http | net/http@go1.25.1 | net/http@go1.25.2 |
| [GO-2025-4011](https://pkg.go.dev/vuln/GO-2025-4011) | Parsing DER payload can cause memory exhaustion in encoding/asn1 | encoding/asn1@go1.25.1 | encoding/asn1@go1.25.2 |
| [GO-2025-4010](https://pkg.go.dev/vuln/GO-2025-4010) | Insufficient validation of bracketed IPv6 hostnames in net/url | net/url@go1.25.1 | net/url@go1.25.2 |
| [GO-2025-4009](https://pkg.go.dev/vuln/GO-2025-4009) | Quadratic complexity when parsing some invalid inputs in encoding/pem | encoding/pem@go1.25.1 | encoding/pem@go1.25.2 |
| [GO-2025-4008](https://pkg.go.dev/vuln/GO-2025-4008) | ALPN negotiation error contains attacker controlled information in crypto/tls | crypto/tls@go1.25.1 | crypto/tls@go1.25.2 |
| [GO-2025-4007](https://pkg.go.dev/vuln/GO-2025-4007) | Quadratic complexity when checking name constraints in crypto/x509 | crypto/x509@go1.25.1 | crypto/x509@go1.25.3 |
| [GO-2025-4006](https://pkg.go.dev/vuln/GO-2025-4006) | Excessive CPU consumption in ParseAddress in net/mail | net/mail@go1.25.1 | net/mail@go1.25.2 |

## Package Results: 5

| ID | Description | Found | Fixed |
|---|---|---|---|
| [GO-2026-6604](https://pkg.go.dev/vuln/GO-2026-6604) | Root.Mkdir(All) can follow junctions out of the root on Windows in os | os@go1.25.1 | os@go1.26.9 |
| [GO-2026-4981](https://pkg.go.dev/vuln/GO-2026-4981) | Crash when handling long CNAME response in net | net@go1.25.1 | net@go1.25.10 |
| [GO-2026-4970](https://pkg.go.dev/vuln/GO-2026-4970) | Root escape via symlink plus trailing slash in os | os@go1.25.1 | os@go1.25.12 |
| [GO-2026-4772](https://pkg.go.dev/vuln/GO-2026-4772) | CVE-2026-33816 in github.com/jackc/pgx | github.com/jackc/pgx/v5@v5.7.1 | github.com/jackc/pgx/v5@v5.9.0 |
| [GO-2026-4771](https://pkg.go.dev/vuln/GO-2026-4771) | CVE-2026-33815 in github.com/jackc/pgx | github.com/jackc/pgx/v5@v5.7.1 | github.com/jackc/pgx/v5@v5.9.0 |

## Module Results: 34

| ID | Description | Found | Fixed |
|---|---|---|---|
| [GO-2026-6600](https://pkg.go.dev/vuln/GO-2026-6600) | Recognize yield as regexp preceder keyword in html/template | stdlib@go1.25.1 | stdlib@go1.26.9 |
| [GO-2026-6599](https://pkg.go.dev/vuln/GO-2026-6599) | Reset context tracking on consecutive template expressions in html/template | stdlib@go1.25.1 | stdlib@go1.26.9 |
| [GO-2026-6355](https://pkg.go.dev/vuln/GO-2026-6355) | Prevent DoS on deadlocked established channel in golang.org/x/crypto/ssh | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.56.0 |
| [GO-2026-6354](https://pkg.go.dev/vuln/GO-2026-6354) | Prevent DoS on deadlocked undecided channel in golang.org/x/crypto/ssh | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.56.0 |
| [GO-2026-6303](https://pkg.go.dev/vuln/GO-2026-6303) | Source-address critical option not enforced for non-public-key auth callbacks in golang.org/x/crypto/ssh | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.55.0 |
| [GO-2026-6091](https://pkg.go.dev/vuln/GO-2026-6091) | Fix Javascript regexp context tracking in html/template | stdlib@go1.25.1 | stdlib@go1.25.13 |
| [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932) | The golang.org/x/crypto/openpgp package is unmaintained, unsafe by design, and has known security issues | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.58.1-0.20261009181718-2f1f834330b2 |
| [GO-2026-5033](https://pkg.go.dev/vuln/GO-2026-5033) | Invoking pathological inputs can lead to client panic in golang.org/x/crypto/ssh/agent | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.52.0 |
| [GO-2026-5023](https://pkg.go.dev/vuln/GO-2026-5023) | Invoking VerifiedPublicKeyCallback permissions skip enforcement in golang.org/x/crypto/ssh | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.52.0 |
| [GO-2026-5021](https://pkg.go.dev/vuln/GO-2026-5021) | Invoking auth bypass via unenforced @revoked status in golang.org/x/crypto/ssh/knownhosts | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.52.0 |
| [GO-2026-5020](https://pkg.go.dev/vuln/GO-2026-5020) | Invoking infinite loop on large channel writes in golang.org/x/crypto/ssh | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.52.0 |
| [GO-2026-5019](https://pkg.go.dev/vuln/GO-2026-5019) | Invoking bypass of FIDO/U2F security keys physical interaction in golang.org/x/crypto/ssh | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.52.0 |
| [GO-2026-5018](https://pkg.go.dev/vuln/GO-2026-5018) | Invoking pathological RSA/DSA parameters may cause DoS in golang.org/x/crypto/ssh | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.52.0 |
| [GO-2026-5017](https://pkg.go.dev/vuln/GO-2026-5017) | Invoking client can cause server deadlock on unexpected responses in golang.org/x/crypto/ssh | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.52.0 |
| [GO-2026-5016](https://pkg.go.dev/vuln/GO-2026-5016) | Invoking memory leak when rejecting channels can lead to DoS in golang.org/x/crypto/ssh | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.52.0 |
| [GO-2026-5015](https://pkg.go.dev/vuln/GO-2026-5015) | Invoking server panic during CheckHostKey/Authenticate in golang.org/x/crypto/ssh | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.52.0 |
| [GO-2026-5014](https://pkg.go.dev/vuln/GO-2026-5014) | Invoking bypass of certificate restrictions in golang.org/x/crypto/ssh | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.52.0 |
| [GO-2026-5013](https://pkg.go.dev/vuln/GO-2026-5013) | Invoking byte arithmetic causes underflow and panic in golang.org/x/crypto/ssh | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.52.0 |
| [GO-2026-5006](https://pkg.go.dev/vuln/GO-2026-5006) | Invoking agent constraints dropped when forwarding keys in golang.org/x/crypto/ssh/agent | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.52.0 |
| [GO-2026-5005](https://pkg.go.dev/vuln/GO-2026-5005) | Invoking key constraints not enforced in golang.org/x/crypto/ssh/agent | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.52.0 |
| [GO-2026-4982](https://pkg.go.dev/vuln/GO-2026-4982) | Bypass of meta content URL escaping causes XSS in html/template | stdlib@go1.25.1 | stdlib@go1.25.10 |
| [GO-2026-4980](https://pkg.go.dev/vuln/GO-2026-4980) | Escaper bypass leads to XSS in html/template | stdlib@go1.25.1 | stdlib@go1.25.10 |
| [GO-2026-4976](https://pkg.go.dev/vuln/GO-2026-4976) | ReverseProxy forwards queries with more than urlmaxqueryparams parameters in net/http/httputil | stdlib@go1.25.1 | stdlib@go1.25.10 |
| [GO-2026-4869](https://pkg.go.dev/vuln/GO-2026-4869) | Unbounded allocation for old GNU sparse in archive/tar | stdlib@go1.25.1 | stdlib@go1.25.9 |
| [GO-2026-4865](https://pkg.go.dev/vuln/GO-2026-4865) | JsBraceDepth Context Tracking Bugs (XSS) in html/template | stdlib@go1.25.1 | stdlib@go1.25.9 |
| [GO-2026-4864](https://pkg.go.dev/vuln/GO-2026-4864) | TOCTOU permits root escape on Linux via Root.Chmod in os in internal/syscall/unix | stdlib@go1.25.1 | stdlib@go1.25.9 |
| [GO-2026-4603](https://pkg.go.dev/vuln/GO-2026-4603) | URLs in meta content attribute actions are not escaped in html/template | stdlib@go1.25.1 | stdlib@go1.25.8 |
| [GO-2026-4342](https://pkg.go.dev/vuln/GO-2026-4342) | Excessive CPU consumption when building archive index in archive/zip | stdlib@go1.25.1 | stdlib@go1.25.6 |
| [GO-2025-4135](https://pkg.go.dev/vuln/GO-2025-4135) | Malformed constraint may cause denial of service in golang.org/x/crypto/ssh/agent | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.45.0 |
| [GO-2025-4134](https://pkg.go.dev/vuln/GO-2025-4134) | Unbounded memory consumption in golang.org/x/crypto/ssh | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.45.0 |
| [GO-2025-4116](https://pkg.go.dev/vuln/GO-2025-4116) | Potential denial of service in golang.org/x/crypto/ssh/agent | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.43.0 |
| [GO-2025-4014](https://pkg.go.dev/vuln/GO-2025-4014) | Unbounded allocation when parsing GNU sparse map in archive/tar | stdlib@go1.25.1 | stdlib@go1.25.2 |
| [GO-2025-3487](https://pkg.go.dev/vuln/GO-2025-3487) | Potential denial of service in golang.org/x/crypto | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.35.0 |
| [GO-2024-3321](https://pkg.go.dev/vuln/GO-2024-3321) | Misuse of connection.serverAuthenticate may cause authorization bypass in golang.org/x/crypto | golang.org/x/crypto@v0.27.0 | golang.org/x/crypto@v0.31.0 |

## Scanner verdict

Your code is affected by 48 vulnerabilities from 2 modules and the Go standard library.
This scan also found 5 vulnerabilities in packages you import and 34
vulnerabilities in modules you require, but your code doesn't appear to call
these vulnerabilities.
