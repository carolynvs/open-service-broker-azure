package postgresql

import (
	"context"
	"fmt"

	"github.com/Azure/azure-service-broker/pkg/azure"
	"github.com/Azure/azure-service-broker/pkg/service"
	_ "github.com/lib/pq" // Postgres SQL driver
	uuid "github.com/satori/go.uuid"
)

const (
	identifierLength = 10
	identifierChars  = lowerAlphaChars + numberChars
)

func (m *module) ValidateProvisioningParameters(
	provisioningParameters interface{},
) error {
	pp, ok := provisioningParameters.(*postgresqlProvisioningParameters)
	if !ok {
		return fmt.Errorf(
			"error casting provisioningParameters as " +
				"postgresqlProvisioningParameters",
		)
	}
	if !azure.IsValidLocation(pp.Location) {
		return service.NewValidationError(
			"location",
			fmt.Sprintf(`invalid location: "%s"`, pp.Location),
		)
	}
	return nil
}

func (m *module) GetProvisioner(string, string) (service.Provisioner, error) {
	return service.NewProvisioner(
		service.NewProvisioningStep("preProvision", m.preProvision),
		service.NewProvisioningStep("deployARMTemplate", m.deployARMTemplate),
		service.NewProvisioningStep("setupDatabase", m.setupDatabase),
	)
}

func (m *module) preProvision(
	ctx context.Context, // nolint: unparam
	provisioningContext interface{},
	provisioningParameters interface{}, // nolint: unparam
) (interface{}, error) {
	pc, ok := provisioningContext.(*postgresqlProvisioningContext)
	if !ok {
		return nil, fmt.Errorf(
			"error casting provisioningContext as postgresqlProvisioningContext",
		)
	}
	pc.ResourceGroupName = uuid.NewV4().String()
	pc.ARMDeploymentName = uuid.NewV4().String()
	pc.ServerName = uuid.NewV4().String()
	pc.AdministratorLoginPassword = generatePassword()
	pc.DatabaseName = generateIdentifier()
	return pc, nil
}

func (m *module) deployARMTemplate(
	ctx context.Context, // nolint: unparam
	provisioningContext interface{},
	provisioningParameters interface{},
) (interface{}, error) {
	pc, ok := provisioningContext.(*postgresqlProvisioningContext)
	if !ok {
		return nil, fmt.Errorf(
			"error casting provisioningContext as postgresqlProvisioningContext",
		)
	}
	pp, ok := provisioningParameters.(*postgresqlProvisioningParameters)
	if !ok {
		return nil, fmt.Errorf(
			"error casting provisioningParameters as " +
				"postgresqlProvisioningParameters",
		)
	}
	outputs, err := m.armDeployer.Deploy(
		pc.ARMDeploymentName,
		pc.ResourceGroupName,
		pp.Location,
		armTemplateBytes,
		// TODO: Values in this map should vary according to the serviceID and planID
		// selected
		map[string]interface{}{
			"administratorLoginPassword": pc.AdministratorLoginPassword,
			"serverName":                 pc.ServerName,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("error deploying ARM template: %s", err)
	}

	fullyQualifiedDomainName, ok := outputs["fullyQualifiedDomainName"].(string)
	if !ok {
		return nil, fmt.Errorf(
			"error retrieving fully qualified domain name from deployment: %s",
			err,
		)
	}
	pc.FullyQualifiedDomainName = fullyQualifiedDomainName

	return pc, nil
}

func (m *module) setupDatabase(
	ctx context.Context, // nolint: unparam
	provisioningContext interface{},
	provisioningParameters interface{}, // nolint: unparam
) (interface{}, error) {
	pc, ok := provisioningContext.(*postgresqlProvisioningContext)
	if !ok {
		return nil, fmt.Errorf(
			"error casting provisioningContext as postgresqlProvisioningContext",
		)
	}

	db, err := getDBConnection(pc)
	if err != nil {
		return nil, err
	}
	defer db.Close() // nolint: errcheck

	_, err = db.Exec(
		fmt.Sprintf("create role %s", pc.DatabaseName),
	)
	if err != nil {
		return nil, fmt.Errorf(`error creating role "%s": %s`, pc.DatabaseName, err)
	}
	_, err = db.Exec(
		fmt.Sprintf("grant %s to postgres", pc.DatabaseName),
	)
	if err != nil {
		return nil, fmt.Errorf(
			`error adding role "%s" to role "postgres": %s`,
			pc.DatabaseName,
			err,
		)
	}
	_, err = db.Exec(
		fmt.Sprintf(
			"create database %s with owner %s",
			pc.DatabaseName,
			pc.DatabaseName,
		),
	)
	if err != nil {
		return nil, fmt.Errorf(
			`error creating database "%s": %s`,
			pc.DatabaseName,
			err,
		)
	}

	return pc, nil
}