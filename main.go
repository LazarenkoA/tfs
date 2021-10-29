package main

import (
	"context"
	"fmt"
	"github.com/goccy/go-graphviz/cgraph"
	"github.com/microsoft/azure-devops-go-api/azuredevops"
	"regexp"
	"sort"
	"strings"

	"github.com/goccy/go-graphviz"
	//"github.com/microsoft/azure-devops-go-api/azuredevops/search"
	"github.com/microsoft/azure-devops-go-api/azuredevops/webapi"
	wit "github.com/microsoft/azure-devops-go-api/azuredevops/workitemtracking"
	kingpin "gopkg.in/alecthomas/kingpin.v2"
	"log"
	"os"
	"strconv"
	"time"
)

const (
	fieldcompletedWork    = "Microsoft.VSTS.Scheduling.CompletedWork"
	fieldoriginalEstimate = "Microsoft.VSTS.Scheduling.OriginalEstimate"
	fieldProject          = "System.TeamProject"
	relCommit             = "ArtifactLink"
	relRelated            = "System.LinkTypes.Related"
	relParent             = "System.LinkTypes.Hierarchy-Reverse"
	relChild              = "System.LinkTypes.Hierarchy-Forward"
	fieldWorkItemType     = "System.WorkItemType"
	stateClose            = "Closed"
	autor                 = "PARMA\\Lazarenko.AN"
)

type SystemField []string

type workItemClient struct {
	connection *azuredevops.Connection
	wiClient   wit.Client
	ctx        context.Context
}

var (
	kp                  *kingpin.Application
	organizationUrl     string
	personalAccessToken string
	projects            string
	auto                *bool
	wikey               *int
	buff                map[*cgraph.Node]int
)

func init() {
	organizationUrl = os.Getenv("tfsurl")
	personalAccessToken = os.Getenv("tfsAccessToken")
	projects = os.Getenv("tfsprojects")

	kp = kingpin.New("TFS", "Автоматизация рутинной работы с TFS")
	auto = kp.Flag("demon", "ПО будет запущено как demon").Short('d').Bool()
	wikey = kp.Flag("graph", "построить граф связей WI").Short('g').Int()

	buff = map[*cgraph.Node]int{}
}

func main() {
	ctx := context.Background()

	if organizationUrl == "" {
		log.Println("Укажите tfsurl в переменных окружения")
		os.Exit(1)
	}

	if personalAccessToken == "" {
		log.Println("Укажите tfsAccessToken в переменных окружения")
		os.Exit(1)
	}

	kp.Parse(os.Args[1:])
	wrapper := new(workItemClient).Create(ctx)

	var err error
	if wrapper.wiClient, err = wit.NewClient(wrapper.ctx, wrapper.connection); err != nil {
		log.Fatal(err)
	}

	if *auto {
		runDemon(wrapper)
	} else if *wikey > 0 {
		filePath, _ := generateGraph(wrapper, wikey)
		fmt.Println(filePath)
		return
	}

	if len(os.Args) < 3 {
		log.Println("Укажите номер задачи и время для списания")
		return
	}

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

	run(wrapper, key, hour)
}

func (this *workItemClient) Create(ctx context.Context) *workItemClient {
	// Create a connection to your organization
	this.connection = azuredevops.NewPatConnection(organizationUrl, personalAccessToken)
	this.ctx = ctx

	return this
}

func (this *workItemClient) CopyWorkItem(sourceWI *wit.WorkItem, hour int64) (*wit.WorkItem, error) {
	sysField := SystemField{"Microsoft.VSTS.Common.ResolvedReason", "Microsoft.VSTS.Common.ResolvedDate",
		"Microsoft.VSTS.Common.ResolvedBy", "System.State", "System.AssignedTo", "System.Reason",
		"Microsoft.VSTS.Scheduling.RemainingWork", "Microsoft.VSTS.Common.ActivatedDate",
		"Microsoft.VSTS.Common.ActivatedBy", fieldcompletedWork, fieldoriginalEstimate}

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
		if *ref.Rel == "ArtifactLink" {
			continue
		}
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
	pathCompletedWork := fmt.Sprintf("/fields/%s", fieldcompletedWork)
	pathOriginalEstimate := fmt.Sprintf("/fields/%s", fieldoriginalEstimate)
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
	})

	return err
}

func (this *workItemClient) getComments(wi wit.WorkItem) []wit.Comment {
	project, _ := (*wi.Fields)[fieldProject].(string)
	comments, err := this.wiClient.GetComments(this.ctx, wit.GetCommentsArgs{
		Project:    &project,
		WorkItemId: wi.Id,
	})
	if err != nil {
		return make([]wit.Comment, 0)
	} else {
		return *comments.Comments
	}
}

func (this *workItemClient) getHours() {
	query := fmt.Sprintf("SELECT [%s] FROM WorkItems "+
		"WHERE [System.AssignedTo] = @Me "+
		"AND [System.ChangedDate] > @StartOfMonth", fieldcompletedWork)
	req, err := this.wiClient.QueryByWiql(this.ctx, wit.QueryByWiqlArgs{
		Wiql: &wit.Wiql{Query: &query},
	})
	hours := .0
	if err == nil {
		Ids := make([]int, 0)
		for _, wi := range *req.WorkItems {
			Ids = append(Ids, *wi.Id)
		}

		wis, err := this.wiClient.GetWorkItems(this.ctx, wit.GetWorkItemsArgs{
			Ids: &Ids,
		})

		if err == nil {
			for _, wi := range *wis {
				updates, _ := this.wiClient.GetUpdates(this.ctx, wit.GetUpdatesArgs{
					Id: wi.Id,
				})
				fmt.Println(updates)

				h, _ := (*wi.Fields)[fieldcompletedWork].(float64)
				hours += h
				fmt.Println(*wi.Id)
			}
		}
	}
	fmt.Println(hours)
}

func (this *workItemClient) getHour(html string) (result int) {
	txt := RemoveHtmlTag(html)

	r := regexp.MustCompile(`[\s]*{([\d]+)}[\s]*`)
	groups := r.FindAllStringSubmatch(txt, -1)
	if len(groups) > 0 {
		result, _ = strconv.Atoi(groups[0][1])
	}

	return result
}

func (this SystemField) in(str string) bool {
	for _, item := range this {
		if item == str {
			return true
		}
	}

	return false
}

func runDemon(wrapper *workItemClient) {
	var err error

	if wrapper.wiClient, err = wit.NewClient(wrapper.ctx, wrapper.connection); err != nil {
		log.Fatal(err)
	}

	for range time.Tick(time.Second * 5) {
		query := fmt.Sprintf("SELECT [System.Id] FROM WorkItems "+
			"WHERE [System.TeamProject] in (%s) "+
			"AND  [System.ChangedDate] > @Today-1", projects)
		req, err := wrapper.wiClient.QueryByWiql(wrapper.ctx, wit.QueryByWiqlArgs{
			Wiql: &wit.Wiql{Query: &query},
		})
		if err == nil {
			Ids := make([]int, 0)
			for _, wi := range *req.WorkItems {
				Ids = append(Ids, *wi.Id)
			}

			wis, err := wrapper.wiClient.GetWorkItems(wrapper.ctx, wit.GetWorkItemsArgs{
				Ids: &Ids,
			})
			if err == nil {
				for _, wi := range *wis {
					comments := wrapper.getComments(wi)
					for _, c := range comments {
						if *c.CreatedBy.UniqueName == autor {
							if hour := wrapper.getHour(*c.Text); hour > 0 {
								project, _ := (*wi.Fields)[fieldProject].(string)
								err := wrapper.wiClient.DeleteComment(wrapper.ctx, wit.DeleteCommentArgs{
									WorkItemId: wi.Id,
									CommentId:  c.Id,
									Project:    &project,
								})
								if err == nil {
									run(wrapper, *wi.Id, hour)
								}
							}
						}
					}
				}
			}
		}
	}
}

func RemoveHtmlTag(in string) string {
	const pattern = `(<\/?[a-zA-A]+?[^>]*\/?>)*`
	r := regexp.MustCompile(pattern)
	groups := r.FindAllString(in, -1)
	// should replace long string first
	sort.Slice(groups, func(i, j int) bool {
		return len(groups[i]) > len(groups[j])
	})
	for _, group := range groups {
		if strings.TrimSpace(group) != "" {
			in = strings.ReplaceAll(in, group, "")
		}
	}
	return in
}

func run(wrapper *workItemClient, key, hour int) {
	wi, _ := wrapper.wiClient.GetWorkItem(wrapper.ctx, wit.GetWorkItemArgs{
		Id:     &key,
		Expand: &wit.WorkItemExpandValues.All,
	})
	if wi == nil {
		log.Printf("WI %v не найден\n", key)
		return
	}

	if newWI, err := wrapper.CopyWorkItem(wi, int64(hour)); err == nil {
		log.Printf("Новый WI: %v", *newWI.Id)
		if err := wrapper.changeState(newWI, stateClose); err != nil {
			log.Printf("Не удалось закрыть wi %v произошла ошибка:\n\t%v\n", *newWI.Id, err)
		}
	} else {
		log.Printf("При копировании wi %d произошла ошибка:\n\t%v\n", key, err)
	}
}

func generateGraph(wrapper *workItemClient, wikey *int) (string, error) {
	g := graphviz.New()
	graph, err := g.SetLayout(graphviz.DOT).Graph()
	if err != nil {
		return "", err
	}
	defer func() {
		graph.Close()
		g.Close()
	}()

	generateGraphRecursively(graph, wrapper, wikey)

	//var buf bytes.Buffer
	//if err := g.Render(graph, "dot", &buf); err != nil {
	//	log.Fatal(err)
	//}
	//fmt.Println(buf.String())

	if err := g.RenderFilename(graph, graphviz.JPG, "C:\\Users\\lazarenko.an\\IdeaProjects\\tfs\\graph.jpg"); err != nil {
		return "", err
	}

	return "", nil
}

func generateGraphRecursively(graph *cgraph.Graph, wrapper *workItemClient, wikey *int) *cgraph.Node {
	wi, _ := wrapper.wiClient.GetWorkItem(wrapper.ctx, wit.GetWorkItemArgs{
		Id:     wikey,
		Expand: &wit.WorkItemExpandValues.All,
	})
	if wi == nil {
		log.Printf("WI %v не найден\n", *wikey)
		return nil
	}

	createNode := func(name string) (result *cgraph.Node, exists bool) {
		if result, _ = graph.Node(name); result == nil {
			result, _ = graph.CreateNode(name)
			exists = false
		} else {
			return result, true
		}

		result.SetStyle("filled")

		switch strings.ToLower((*wi.Fields)[fieldWorkItemType].(string)) {
		case "bug":
			result.SetFillColor("#ff9494")
		case "issue":
			result.SetFillColor("#ffa500")
		case "task":
			result.SetFillColor("#fde910")
		case "requirement":
			result.SetFillColor("#6666ff")
		}

		return result, exists
	}

	main, exists := createNode(strconv.Itoa(*wikey))
	if exists {
		return main
	}
	for i, r := range *wi.Relations {
		if *r.Rel == relCommit {
			continue
		}

		urlsplited := strings.Split(*r.Url, "/")
		key := urlsplited[len(urlsplited)-1]
		newwikey, err := strconv.Atoi(key)
		if err != nil {
			continue
		}

		if graph.NumberNodes() > 100 {
			break
		}

		m := generateGraphRecursively(graph, wrapper, &newwikey)
		if m == nil {
			continue
		}

		//var m *cgraph.Node
		//var exists bool
		//if m, exists = createNode(key); !exists {
		//	generateGraphRecursively(graph, wrapper, &newwikey)
		//}

		// создаем связь
		e, err := graph.CreateEdge(fmt.Sprintf("relation_%d", i), main, m)
		if err != nil {
			return nil
		}

		buff[main]++
		//m.SetComment(fmt.Sprintf("%d (%d)", *wikey, buff[m]))

		switch *r.Rel {
		case relRelated:
			e.SetColor("#0000ff")
		case relParent:
			e.SetColor("red")
		case relChild:
			e.SetColor("#008000")
		}

		e.SetLabel((*r.Attributes)["name"].(string))
	}

	return main
}
