"""Provider-reported usage; published rates produce an estimate, not a bill."""
import json
import math
import threading

INPUT_USD_PER_MILLION = 0.042
OUTPUT_USD_PER_MILLION = 0.0
PRICING_URL = "https://docs.typesafe.ai/models"
PRICING_VERIFIED = "2026-09-20"


class UsageMeter:
    def __init__(self, input_rate=INPUT_USD_PER_MILLION, output_rate=OUTPUT_USD_PER_MILLION):
        for rate in (input_rate, output_rate):
            if isinstance(rate, bool) or not isinstance(rate, (int, float)) or not math.isfinite(rate) or rate < 0:
                raise ValueError("Token prices must be finite nonnegative numbers")
        self.input_rate, self.output_rate = input_rate, output_rate
        self.lock = threading.Lock()
        self.attempts = self.finished = self.metered = 0
        self.input_tokens = self.output_tokens = self.missing_output = 0

    def begin(self):
        with self.lock:
            self.attempts += 1

    def record(self, raw):
        try:
            usage = json.loads(raw)["usage"] if raw else {}
            incoming, outgoing = usage.get("input_tokens"), usage.get("output_tokens")
        except (ValueError, TypeError, KeyError, AttributeError):
            incoming = outgoing = None
        valid_in = type(incoming) is int and incoming >= 0
        valid_out = type(outgoing) is int and outgoing >= 0
        with self.lock:
            self.finished += 1
            if valid_in:
                self.metered += 1
                self.input_tokens += incoming
            if valid_out:
                self.output_tokens += outgoing
            else:
                self.missing_output += 1

    def snapshot(self):
        with self.lock:
            cost = (self.input_tokens * self.input_rate + self.output_tokens * self.output_rate) / 1_000_000
            return {"request_attempts": self.attempts, "requests_finished": self.finished,
                    "in_flight": self.attempts - self.finished, "metered_requests": self.metered,
                    "unmetered_requests": self.finished - self.metered,
                    "missing_output_usage": self.missing_output,
                    "input_tokens": self.input_tokens, "output_tokens": self.output_tokens,
                    "estimated_cost_usd": round(cost, 12), "input_usd_per_million": self.input_rate,
                    "output_usd_per_million": self.output_rate,
                    "cost_complete": self.finished == self.metered and self.attempts == self.finished and (self.output_rate == 0 or self.missing_output == 0),
                    "pricing_source": PRICING_URL, "pricing_verified": PRICING_VERIFIED,
                    "basis": "Provider-reported tokens × configured rates; not an invoice"}
