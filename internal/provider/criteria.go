package provider

// These choice meanings carry forward the original TypeSafe classification
// contract. Enum names alone are not an operational-risk specification.
var choiceCriteria = map[string]map[string]string{
	"importance": {
		"important": "Investigate: current failure, customer impact, silent data loss, resource exhaustion, dangerous change or meaningful security risk.",
		"routine":   "Expected operation, successful recovery, harmless client error, or informational message with no action needed.",
		"uncertain": "Insufficient evidence to confidently judge whether investigation is needed.",
	},
	"severity": {
		"noise": "Routine or recovered", "info": "Informational, no service impact", "degraded": "Reliability, capacity or correctness at risk", "impact": "Customers currently affected", "outage": "Core service unavailable",
	},
	"category": {
		"deploy": "Rollout or version change", "capacity": "Resource pressure, queues, lag or overload", "dependency": "Upstream failure", "security": "Abuse, credential attack or dangerous authorization", "fraud": "Suspicious financial or account activity requiring review; do not infer guilt", "data": "Missing, corrupted or inconsistent data", "config": "Configuration or certificate problem", "transient": "Recovered temporary failure", "routine": "Normal operation", "unknown": "Insufficient evidence",
	},
}
