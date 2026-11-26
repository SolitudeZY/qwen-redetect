package handler

import "testing"

func TestImageValidityReviewInvalidatesWarning(t *testing.T) {
	tests := []struct {
		name            string
		parsed          map[string]interface{}
		wantInvalidates bool
		wantError       bool
	}{
		{
			name: "清晰滑坡伴随局部云雾时保留告警",
			parsed: map[string]interface{}{
				"image_invalid":    false,
				"disaster_visible": true,
			},
		},
		{
			name: "主要区域完全不可见且无灾害证据时取消告警",
			parsed: map[string]interface{}{
				"image_invalid":    true,
				"disaster_visible": false,
			},
			wantInvalidates: true,
		},
		{
			name: "复核结果自相矛盾时保留告警",
			parsed: map[string]interface{}{
				"image_invalid":    true,
				"disaster_visible": true,
			},
		},
		{
			name: "缺少必要字段时返回错误并保留告警",
			parsed: map[string]interface{}{
				"image_invalid": true,
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			review, err := parseImageValidityReview(tt.parsed)
			if (err != nil) != tt.wantError {
				t.Fatalf("parseImageValidityReview() error = %v, wantError = %v", err, tt.wantError)
			}
			if got := review.invalidatesWarning(); got != tt.wantInvalidates {
				t.Fatalf("invalidatesWarning() = %v, want %v", got, tt.wantInvalidates)
			}
		})
	}
}
