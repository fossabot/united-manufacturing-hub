package controllers

import (
	"github.com/gin-gonic/gin"
	"github.com/united-manufacturing-hub/united-manufacturing-hub/cmd/factoryinsight/helpers"
	"github.com/united-manufacturing-hub/united-manufacturing-hub/cmd/factoryinsight/v2/models"
	"github.com/united-manufacturing-hub/united-manufacturing-hub/cmd/factoryinsight/v2/services"
)

func GetKpisDataHandler(c *gin.Context) {
	var request models.GetKpisDataRequest

	err := c.BindUri(&request)
	if err != nil {
		helpers.HandleInvalidInputError(c, err)
		return
	}

	// Check if the user has access to that resource
	err = helpers.CheckIfUserIsAllowed(c, request.EnterpriseName)
	if err != nil {
		return
	}

	switch request.KpisMethod {
	case models.OeeKpi:
		services.ProcessOeeKpiRequest(c, request)
	case models.AvailabilityKpi:
		services.ProcessAvailabilityKpiRequest(c, request)
	case models.PerformanceKpi:
		services.ProcessPerformanceKpiRequest(c, request)
	case models.QualityKpi:
		services.ProcessQualityKpiRequest(c, request)
	default:
		helpers.HandleInvalidInputError(c, err)
		return
	}
}
