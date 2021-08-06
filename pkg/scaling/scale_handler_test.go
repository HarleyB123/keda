package scaling

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	kedav1alpha1 "github.com/kedacore/keda/v2/api/v1alpha1"
	"github.com/kedacore/keda/v2/pkg/mock/mock_client"
	mock_scalers "github.com/kedacore/keda/v2/pkg/mock/mock_scaler"
	"github.com/kedacore/keda/v2/pkg/scalers"
	"github.com/kedacore/keda/v2/pkg/scaling/executor"
	"k8s.io/client-go/tools/record"
	"k8s.io/metrics/pkg/apis/external_metrics"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stretchr/testify/assert"
	"k8s.io/api/autoscaling/v2beta2"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestTargetAverageValue(t *testing.T) {
	// count = 0
	specs := []v2beta2.MetricSpec{}
	targetAverageValue := getTargetAverageValue(specs)
	assert.Equal(t, int64(0), targetAverageValue)
	// 1 1
	specs = []v2beta2.MetricSpec{
		createMetricSpec(1),
		createMetricSpec(1),
	}
	targetAverageValue = getTargetAverageValue(specs)
	assert.Equal(t, int64(1), targetAverageValue)
	// 5 5 3
	specs = []v2beta2.MetricSpec{
		createMetricSpec(5),
		createMetricSpec(5),
		createMetricSpec(3),
	}
	targetAverageValue = getTargetAverageValue(specs)
	assert.Equal(t, int64(4), targetAverageValue)

	// 5 5 4
	specs = []v2beta2.MetricSpec{
		createMetricSpec(5),
		createMetricSpec(5),
		createMetricSpec(3),
	}
	targetAverageValue = getTargetAverageValue(specs)
	assert.Equal(t, int64(4), targetAverageValue)
}

func TestCheckScaledObjectScalersWithError(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := mock_client.NewMockClient(ctrl)
	recorder := record.NewFakeRecorder(1)

	scaleHandler := &scaleHandler{
		client:            client,
		logger:            logf.Log.WithName("scalehandler"),
		scaleLoopContexts: &sync.Map{},
		scaleExecutor:     executor.NewScaleExecutor(client, nil, nil, recorder),
		globalHTTPTimeout: 5 * time.Second,
		recorder:          recorder,
	}
	scaler := mock_scalers.NewMockScaler(ctrl)
	scalers := []scalers.Scaler{scaler}
	scaledObject := &kedav1alpha1.ScaledObject{}

	scaler.EXPECT().IsActive(gomock.Any()).Return(false, errors.New("Some error"))
	scaler.EXPECT().Close()

	isActive, isError := scaleHandler.isScaledObjectActive(context.TODO(), scalers, scaledObject)

	assert.Equal(t, false, isActive)
	assert.Equal(t, true, isError)
}

func TestCheckScaledObjectFindFirstActiveIgnoringOthers(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := mock_client.NewMockClient(ctrl)
	recorder := record.NewFakeRecorder(1)

	scaleHandler := &scaleHandler{
		client:            client,
		logger:            logf.Log.WithName("scalehandler"),
		scaleLoopContexts: &sync.Map{},
		scaleExecutor:     executor.NewScaleExecutor(client, nil, nil, recorder),
		globalHTTPTimeout: 5 * time.Second,
		recorder:          recorder,
	}

	activeScaler := mock_scalers.NewMockScaler(ctrl)
	failingScaler := mock_scalers.NewMockScaler(ctrl)
	scalers := []scalers.Scaler{activeScaler, failingScaler}
	scaledObject := &kedav1alpha1.ScaledObject{}

	metricsSpecs := []v2beta2.MetricSpec{createMetricSpec(1)}

	activeScaler.EXPECT().IsActive(gomock.Any()).Return(true, nil)
	activeScaler.EXPECT().GetMetricSpecForScaling().Times(2).Return(metricsSpecs)
	activeScaler.EXPECT().Close()
	failingScaler.EXPECT().Close()

	isActive, isError := scaleHandler.isScaledObjectActive(context.TODO(), scalers, scaledObject)

	assert.Equal(t, true, isActive)
	assert.Equal(t, false, isError)
}

func createMetricSpec(averageValue int) v2beta2.MetricSpec {
	qty := resource.NewQuantity(int64(averageValue), resource.DecimalSI)
	return v2beta2.MetricSpec{
		External: &v2beta2.ExternalMetricSource{
			Target: v2beta2.MetricTarget{
				AverageValue: qty,
			},
		},
	}
}

func TestIsScaledJobActive(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := mock_client.NewMockClient(ctrl)
	recorder := record.NewFakeRecorder(1)

	scaleHandler := &scaleHandler{
		client:            client,
		logger:            logf.Log.WithName("scalehandler"),
		scaleLoopContexts: &sync.Map{},
		scaleExecutor:     executor.NewScaleExecutor(client, nil, nil, recorder),
		globalHTTPTimeout: 5 * time.Second,
		recorder:          recorder,
	}

	// Keep the current behavior
	// Assme 1 trigger only
	scaledJobSingle := createScaledObject(100, "") // testing default = max
	scalerSingle := []scalers.Scaler{
		createScaler(ctrl, int64(20), int32(2), true),
	}

	isActive, queueLength, maxValue := scaleHandler.isScaledJobActive(context.TODO(), scalerSingle, scaledJobSingle)
	assert.Equal(t, true, isActive)
	assert.Equal(t, int64(20), queueLength)
	assert.Equal(t, int64(10), maxValue)

	// Non-Active trigger only
	scalerSingle = []scalers.Scaler{
		createScaler(ctrl, int64(0), int32(2), false),
	}

	isActive, queueLength, maxValue = scaleHandler.isScaledJobActive(context.TODO(), scalerSingle, scaledJobSingle)
	assert.Equal(t, false, isActive)
	assert.Equal(t, int64(0), queueLength)
	assert.Equal(t, int64(0), maxValue)

	// Test the valiation
	scalerTestDatam := []scalerTestData{
		newScalerTestData(100, "max", 20, 1, true, 10, 2, true, 5, 3, true, 7, 4, false, true, 20, 20),
		newScalerTestData(100, "min", 20, 1, true, 10, 2, true, 5, 3, true, 7, 4, false, true, 5, 2),
		newScalerTestData(100, "avg", 20, 1, true, 10, 2, true, 5, 3, true, 7, 4, false, true, 12, 9),
		newScalerTestData(100, "sum", 20, 1, true, 10, 2, true, 5, 3, true, 7, 4, false, true, 35, 27),
	}

	for index, scalerTestData := range scalerTestDatam {
		scaledJob := createScaledObject(scalerTestData.MaxReplicaCount, scalerTestData.MultipleScalersOption)
		scalers := []scalers.Scaler{
			createScaler(ctrl, scalerTestData.Scaler1QueueLength, scalerTestData.Scaler1AverageValue, scalerTestData.Scaler1IsActive),
			createScaler(ctrl, scalerTestData.Scaler2QueueLength, scalerTestData.Scaler2AverageValue, scalerTestData.Scaler2IsActive),
			createScaler(ctrl, scalerTestData.Scaler3QueueLength, scalerTestData.Scaler3AverageValue, scalerTestData.Scaler3IsActive),
			createScaler(ctrl, scalerTestData.Scaler4QueueLength, scalerTestData.Scaler4AverageValue, scalerTestData.Scaler4IsActive),
		}
		fmt.Printf("index: %d", index)
		isActive, queueLength, maxValue = scaleHandler.isScaledJobActive(context.TODO(), scalers, scaledJob)
		//	assert.Equal(t, 5, index)
		assert.Equal(t, scalerTestData.ResultIsActive, isActive)
		assert.Equal(t, scalerTestData.ResultQueueLength, queueLength)
		assert.Equal(t, scalerTestData.ResultMaxValue, maxValue)
	}
}

func newScalerTestData(
	maxReplicaCount int, //nolint:golint,unparam
	multipleScalersOption string,
	scaler1QueueLength, //nolint:golint,unparam
	scaler1AverageValue int, //nolint:golint,unparam
	scaler1IsActive bool, //nolint:golint,unparam
	scaler2QueueLength, //nolint:golint,unparam
	scaler2AverageValue int, //nolint:golint,unparam
	scaler2IsActive bool, //nolint:golint,unparam
	scaler3QueueLength, //nolint:golint,unparam
	scaler3AverageValue int, //nolint:golint,unparam
	scaler3IsActive bool, //nolint:golint,unparam
	scaler4QueueLength, //nolint:golint,unparam
	scaler4AverageValue int, //nolint:golint,unparam
	scaler4IsActive bool, //nolint:golint,unparam
	resultIsActive bool, //nolint:golint,unparam
	resultQueueLength,
	resultMaxLength int) scalerTestData {
	return scalerTestData{
		MaxReplicaCount:       int32(maxReplicaCount),
		MultipleScalersOption: multipleScalersOption,
		Scaler1QueueLength:    int64(scaler1QueueLength),
		Scaler1AverageValue:   int32(scaler1AverageValue),
		Scaler1IsActive:       scaler1IsActive,
		Scaler2QueueLength:    int64(scaler2QueueLength),
		Scaler2AverageValue:   int32(scaler2AverageValue),
		Scaler2IsActive:       scaler2IsActive,
		Scaler3QueueLength:    int64(scaler3QueueLength),
		Scaler3AverageValue:   int32(scaler3AverageValue),
		Scaler3IsActive:       scaler3IsActive,
		Scaler4QueueLength:    int64(scaler4QueueLength),
		Scaler4AverageValue:   int32(scaler4AverageValue),
		Scaler4IsActive:       scaler4IsActive,
		ResultIsActive:        resultIsActive,
		ResultQueueLength:     int64(resultQueueLength),
		ResultMaxValue:        int64(resultMaxLength),
	}
}

type scalerTestData struct {
	MaxReplicaCount       int32
	MultipleScalersOption string
	Scaler1QueueLength    int64
	Scaler1AverageValue   int32
	Scaler1IsActive       bool
	Scaler2QueueLength    int64
	Scaler2AverageValue   int32
	Scaler2IsActive       bool
	Scaler3QueueLength    int64
	Scaler3AverageValue   int32
	Scaler3IsActive       bool
	Scaler4QueueLength    int64
	Scaler4AverageValue   int32
	Scaler4IsActive       bool
	ResultIsActive        bool
	ResultQueueLength     int64
	ResultMaxValue        int64
}

func createScaledObject(maxReplicaCount int32, multipleScalersOption string) *kedav1alpha1.ScaledJob {
	if multipleScalersOption != "" {
		return &kedav1alpha1.ScaledJob{
			Spec: kedav1alpha1.ScaledJobSpec{
				MaxReplicaCount: &maxReplicaCount,
				ScalingStrategy: kedav1alpha1.ScalingStrategy{
					MultipleScalersOption: multipleScalersOption,
				},
			},
		}
	}
	return &kedav1alpha1.ScaledJob{
		Spec: kedav1alpha1.ScaledJobSpec{
			MaxReplicaCount: &maxReplicaCount,
		},
	}
}

func createScaler(ctrl *gomock.Controller, queueLength int64, averageValue int32, isActive bool) *mock_scalers.MockScaler {
	metricName := "queueLength"
	scaler := mock_scalers.NewMockScaler(ctrl)
	metricsSpecs := []v2beta2.MetricSpec{createMetricSpec(int(averageValue))}
	metrics := []external_metrics.ExternalMetricValue{
		{
			MetricName: metricName,
			Value:      *resource.NewQuantity(queueLength, resource.DecimalSI),
		},
	}
	scaler.EXPECT().IsActive(gomock.Any()).Return(isActive, nil)
	scaler.EXPECT().GetMetricSpecForScaling().Return(metricsSpecs)
	scaler.EXPECT().GetMetrics(gomock.Any(), metricName, nil).Return(metrics, nil)
	scaler.EXPECT().Close()
	return scaler
}
