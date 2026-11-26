package handler

import "fmt"

type imageValidityReview struct {
	ImageInvalid    bool
	DisasterVisible bool
	Reason          string
}

func parseImageValidityReview(parsed map[string]interface{}) (imageValidityReview, error) {
	imageInvalid, ok := parsed["image_invalid"].(bool)
	if !ok {
		return imageValidityReview{}, fmt.Errorf("缺少布尔字段 image_invalid")
	}
	disasterVisible, ok := parsed["disaster_visible"].(bool)
	if !ok {
		return imageValidityReview{}, fmt.Errorf("缺少布尔字段 disaster_visible")
	}
	reason, _ := parsed["reason"].(string)
	return imageValidityReview{
		ImageInvalid:    imageInvalid,
		DisasterVisible: disasterVisible,
		Reason:          reason,
	}, nil
}

// 只有图像整体不可用且看不到明确灾害证据时，才允许取消主模型告警。
func (r imageValidityReview) invalidatesWarning() bool {
	return r.ImageInvalid && !r.DisasterVisible
}
