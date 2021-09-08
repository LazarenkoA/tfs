package main

import (
	"context"
	"fmt"
	"github.com/microsoft/azure-devops-go-api/azuredevops"
	"github.com/microsoft/azure-devops-go-api/azuredevops/webapi"
	wit "github.com/microsoft/azure-devops-go-api/azuredevops/workitemtracking"
	"log"
	"strconv"

	// kingpin "gopkg.in/alecthomas/kingpin.v2"
	"os"
)

const (
	completedWork    = "Microsoft.VSTS.Scheduling.CompletedWork"
	originalEstimate = "Microsoft.VSTS.Scheduling.OriginalEstimate"
	stateClose       = "Closed"
)

type SystemField []string

type workItemClient struct {
	wiClient wit.Client
	ctx      context.Context
}

var (
	//kp *kingpin.Application
	organizationUrl     string
	personalAccessToken string
)

func init() {
	organizationUrl = os.Getenv("tfsurl")
	personalAccessToken = os.Getenv("tfsAccessToken")

	//kp = kingpin.New("TFS", "Автоматизация рутинной работы с TFS")
	//LogLevel = kp.Flag("LogLevel", "Уровень логирования от 2 до 5\n"+
	//	"\t2 - ошибка\n"+
	//	"\t3 - предупреждение\n"+
	//	"\t4 - информация\n"+
	//	"\t5 - дебаг\n").
	//	Short('l').Default("3").Int()
}

func main() {
	if len(os.Args) < 3 {
		log.Println("Укажите номер задачи и время для списания")
		os.Exit(1)
	}

	if organizationUrl == "" {
		log.Println("Укажите tfsurl в переменных окружения")
		os.Exit(1)
	}

	if personalAccessToken == "" {
		log.Println("Укажите tfsAccessToken в переменных окружения")
		os.Exit(1)
	}

	//kp.Parse(os.Args[1:])

	ctx := context.Background()
	wrapper := new(workItemClient).Create(ctx)

	key, err := strconv.Atoi(os.Args[1])
	if err != nil {
		log.Printf("Укажите номер задачи корректно, вы указали %v\n", os.Args[1])
		os.Exit(1)
	}
	hour, err := strconv.Atoi(os.Args[2])
	if err != nil {
		log.Printf("Укажите время (в часах) корректно, вы указали %v\n", os.Args[2])
		os.Exit(1)
	}

	wi, _ := wrapper.wiClient.GetWorkItem(ctx, wit.GetWorkItemArgs{
		Id:     &key,
		Expand: &wit.WorkItemExpandValues.All,
	})

	if newWI, err := wrapper.CopyWorkItem(wi, int64(hour)); err == nil {
		log.Printf("Новый WI: %v", *newWI.Id)
		if err := wrapper.changeState(newWI, stateClose); err != nil {
			log.Printf("Не удалось закрыть wi %v произошла ошибка:\n\t%v\n", *newWI.Id, err)
		}
	} else {
		log.Printf("При копировании wi %q произошла ошибка:\n\t%v\n", key, err)
	}

}

func (this *workItemClient) Create(ctx context.Context) *workItemClient {
	// Create a connection to your organization
	connection := azuredevops.NewPatConnection(organizationUrl, personalAccessToken)

	var err error
	if this.wiClient, err = wit.NewClient(ctx, connection); err != nil {
		log.Fatal(err)
	}

	this.ctx = ctx

	return this
}

func (this *workItemClient) CopyWorkItem(sourceWI *wit.WorkItem, hour int64) (*wit.WorkItem, error) {
	sysField := SystemField{"System.State", "System.Reason", "Microsoft.VSTS.Common.ActivatedDate", "Microsoft.VSTS.Common.ActivatedBy", completedWork, originalEstimate}

	project, _ := (*sourceWI.Fields)["System.TeamProject"].(string)
	witype, _ := (*sourceWI.Fields)["System.WorkItemType"].(string)

	dao := make([]webapi.JsonPatchOperation, 0)

	// переносим поля
	for k, v := range *sourceWI.Fields {
		key := fmt.Sprintf("/fields/%s", k)
		if sysField.in(k) {
			continue
		}
		dao = append(dao, webapi.JsonPatchOperation{
			Op:    &webapi.OperationValues.Add,
			Path:  &key,
			Value: v,
		})
	}

	// переносим связи
	relPath := "/relations/-"
	for _, ref := range *sourceWI.Relations {
		dao = append(dao, webapi.JsonPatchOperation{
			Op:   &webapi.OperationValues.Add,
			Path: &relPath,
			Value: map[string]interface{}{
				"Rel": ref.Rel,
				"Url": ref.Url,
			},
		})
	}

	// добавляем связь с исходной задачей
	dao = append(dao, webapi.JsonPatchOperation{
		Op:   &webapi.OperationValues.Add,
		Path: &relPath,
		Value: map[string]interface{}{
			"Rel": "System.LinkTypes.Related", // Related
			"Url": sourceWI.Url,
		},
	})

	// устанавливаем время для списания
	pathCompletedWork := fmt.Sprintf("/fields/%s", completedWork)
	pathOriginalEstimate := fmt.Sprintf("/fields/%s", originalEstimate)
	dao = append(dao, webapi.JsonPatchOperation{
		Op:    &webapi.OperationValues.Add,
		Path:  &pathCompletedWork,
		Value: hour,
	})
	dao = append(dao, webapi.JsonPatchOperation{
		Op:    &webapi.OperationValues.Add,
		Path:  &pathOriginalEstimate,
		Value: hour,
	})

	wi, err := this.wiClient.CreateWorkItem(this.ctx, wit.CreateWorkItemArgs{
		Document: &dao,
		Project:  &project,
		Type:     &witype,
	})

	return wi, err
}

func (this *workItemClient) changeState(WI *wit.WorkItem, newStare string) error {
	state := "/fields/System.State"
	resolvedReason := "/fields/Microsoft.VSTS.Common.ResolvedReason"
	dao := []webapi.JsonPatchOperation{
		{Op: &webapi.OperationValues.Add, Path: &state, Value: newStare},
		{Op: &webapi.OperationValues.Add, Path: &resolvedReason, Value: ""},
	}

	_, err := this.wiClient.UpdateWorkItem(this.ctx, wit.UpdateWorkItemArgs{
		Document: &dao,
		Id:       WI.Id,
		//Project:               nil,
	})

	return err
}

func (this SystemField) in(str string) bool {
	for _, item := range this {
		if item == str {
			return true
		}
	}

	return false
}
