package deepseek

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"

	"rllmx/app/protocol/keccak"
)

// The proof of work's ceiling, as the reference sets it: a challenge asking for
// more is refused rather than searched.
const maxPoWDifficulty = 250000

// powChunk is how many nonces pass between context checks.
const powChunk = 1024

// errPoW means the challenge was malformed or no nonce was found in the budget.
var errPoW = errors.New("deepseek: the proof of work was not solved")

// challenge is one DeepSeekHashV1 challenge. Every field is checked before the
// search: it arrives in an answer this router does not control.
type challenge struct {
	Algorithm  string `json:"algorithm"`
	Challenge  string `json:"challenge"`
	Salt       string `json:"salt"`
	Signature  string `json:"signature"`
	TargetPath string `json:"target_path"`
	Difficulty int    `json:"difficulty"`
	ExpireAt   int64  `json:"expire_at"`
}

// parseChallenge reads the challenge out of the service's answer.
func parseChallenge(raw []byte) (challenge, bool) {
	var body struct {
		Data struct {
			BizData struct {
				Challenge challenge `json:"challenge"`
			} `json:"biz_data"`
		} `json:"data"`
		BizData struct {
			Challenge challenge `json:"challenge"`
		} `json:"biz_data"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return challenge{}, false
	}
	c := body.Data.BizData.Challenge
	if c.Algorithm == "" {
		c = body.BizData.Challenge
	}
	if c.Algorithm != "DeepSeekHashV1" || c.Salt == "" || c.Signature == "" || c.TargetPath == "" {
		return challenge{}, false
	}
	if len(c.Challenge) != 64 {
		return challenge{}, false
	}
	if _, err := hex.DecodeString(c.Challenge); err != nil {
		return challenge{}, false
	}
	if c.Difficulty < 1 || c.Difficulty > maxPoWDifficulty || c.ExpireAt < 0 {
		return challenge{}, false
	}
	return c, true
}

// solve finds the first nonce whose DeepSeekHashV1 digest meets the challenge.
// The hash is SHA3-256 with round 0 skipped, over "salt_expireAt_nonce".
func solve(ctx context.Context, c challenge) (int, bool) {
	target, err := hex.DecodeString(c.Challenge)
	if err != nil || len(target) != 32 {
		return 0, false
	}
	prefix := c.Salt + "_" + strconv.FormatInt(c.ExpireAt, 10) + "_"
	for nonce := 0; nonce < c.Difficulty; nonce++ {
		if nonce%powChunk == 0 && ctx.Err() != nil {
			return 0, false
		}
		sum := keccak.Sum256([]byte(prefix+strconv.Itoa(nonce)), 1)
		if string(sum[:]) == string(target) {
			return nonce, true
		}
	}
	return 0, false
}

// answer is the base64 JSON the completion request carries in its proof header.
func answer(c challenge, nonce int) (string, error) {
	raw, err := json.Marshal(struct {
		Algorithm  string `json:"algorithm"`
		Challenge  string `json:"challenge"`
		Salt       string `json:"salt"`
		Answer     int    `json:"answer"`
		Signature  string `json:"signature"`
		TargetPath string `json:"target_path"`
	}{c.Algorithm, c.Challenge, c.Salt, nonce, c.Signature, c.TargetPath})
	if err != nil {
		return "", errPoW
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// powHeader is the header the completion call carries the answer in.
const powHeader = "X-Ds-Pow-Response"
