package scalers

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"

	kedautil "github.com/kedacore/keda/v2/pkg/util"
	v2beta2 "k8s.io/api/autoscaling/v2beta2"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/metrics/pkg/apis/external_metrics"
)

type stockScaler struct {
	metadata   *stockMetadata
	httpClient *http.Client
}

type stockMetadata struct {
	threshold int64
	host      string
}

type stockData struct {
	NumberComments int64   `json:"no_of_comments"`
	Sentiment      string  `json:"sentiment"`
	SentimentScore float64 `json:"sentiment_score"`
	Ticker         string  `json:"ticker"`
}

func NewStockScaler(config *ScalerConfig) (Scaler, error) {

	httpClient := kedautil.CreateHTTPClient(config.GlobalHTTPTimeout)

	stockMetadata, err := parseStockMetadata(config)
	if err != nil {
		return nil, fmt.Errorf("error parsing stock metadata: %s", err)
	}

	return &stockScaler{
		metadata:   stockMetadata,
		httpClient: httpClient,
	}, nil
}

func parseStockMetadata(config *ScalerConfig) (*stockMetadata, error) {

	meta := stockMetadata{}

	if val, ok := config.TriggerMetadata["threshold"]; ok && val != "" {
		threshold, err := strconv.Atoi(val)
		if err != nil {
			return nil, fmt.Errorf("threshold: error parsing threshold %s", err.Error())
		} else {
			meta.threshold = int64(threshold)
		}
	}

	if val, ok := config.TriggerMetadata["host"]; ok {
		_, err := url.ParseRequestURI(val)
		if err != nil {
			return nil, fmt.Errorf("invalid URL: %s", err)
		}
		meta.host = val
	} else {
		return nil, fmt.Errorf("no host URI given")
	}

	return &meta, nil
}

func (s *stockScaler) IsActive(ctx context.Context) (bool, error) {
	numComments, err := s.getStock()
	if err != nil {
		return false, err
	}

	return (int64(numComments)) > s.metadata.threshold, nil

}

func (s *stockScaler) GetMetricSpecForScaling() []v2beta2.MetricSpec {
	targetMetricValue := resource.NewQuantity(int64(s.metadata.threshold), resource.DecimalSI)
	externalMetric := &v2beta2.ExternalMetricSource{
		Metric: v2beta2.MetricIdentifier{
			Name: kedautil.NormalizeString(fmt.Sprintf("%s", "stock")),
		},
		Target: v2beta2.MetricTarget{
			Type:         v2beta2.AverageValueMetricType,
			AverageValue: targetMetricValue,
		},
	}
	metricSpec := v2beta2.MetricSpec{External: externalMetric, Type: externalMetricType}
	return []v2beta2.MetricSpec{metricSpec}
}

func (s *stockScaler) GetMetrics(ctx context.Context, metricName string, metricSelector labels.Selector) ([]external_metrics.ExternalMetricValue, error) {

	comments, _ := s.getStock()

	metric := external_metrics.ExternalMetricValue{
		MetricName: metricName,
		Value:      *resource.NewQuantity(int64(comments), resource.DecimalSI),
		Timestamp:  metav1.Now(),
	}

	return append([]external_metrics.ExternalMetricValue{}, metric), nil
}

func (s *stockScaler) Close() error {
	return nil
}

func (s *stockScaler) getJSONData(out interface{}) error {

	request, err := s.httpClient.Get(s.metadata.host)
	if err != nil {
		return err
	}

	body, err := ioutil.ReadAll(request.Body)
	if err != nil {
		return err
	}
	err = json.Unmarshal(body, &out)
	if err != nil {
		return err
	}
	return nil
}

func (s *stockScaler) getStock() (int, error) {

	var numComments int

	var sDat []stockData
	err := s.getJSONData(&sDat)

	if err != nil {
		return 100, err
	}

	numComments = int(sDat[0].NumberComments)

	return numComments, nil
}
