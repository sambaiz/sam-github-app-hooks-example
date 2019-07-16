package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/secretsmanager"
	jwt "github.com/dgrijalva/jwt-go"
	"github.com/google/go-github/v26/github"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
)

var (
	logger        *zap.Logger
	appID         = os.Getenv("GITHUB_APP_ID")
	privateKeyArn = os.Getenv("PRIVATE_KEY_SECRET_ARN")
	webhookSecret = os.Getenv("WEBHOOK_SECRET")
)

func initLogger() {
	zapLogger, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	logger = zapLogger
}

func getSecret(key string) (string, error) {
	svc := secretsmanager.New(session.New())
	input := &secretsmanager.GetSecretValueInput{
		SecretId:     aws.String(key),
		VersionStage: aws.String("AWSCURRENT"),
	}
	value, err := svc.GetSecretValue(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			logger.Error("Failed to get secret", zap.String("code", aerr.Code()), zap.Error(aerr))
		}
		return "", err
	}
	if value.SecretString == nil {
		return "", errors.New("value is nil")
	}
	return *value.SecretString, nil
}

func newGitHubClient(ctx context.Context, installationID int64) (*github.Client, error) {
	now := time.Now()
	payload := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iat": now.Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
		"iss": appID,
	})
	pem, err := getSecret(privateKeyArn)
	if err != nil {
		return nil, err
	}
	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(pem))
	if err != nil {
		return nil, err
	}
	token, err := payload.SignedString(privateKey)
	if err != nil {
		return nil, err
	}
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	installationToken, _, err := client.Apps.CreateInstallationToken(ctx, installationID)
	if _, _, err := client.Apps.Get(ctx, ""); err != nil {
		return nil, fmt.Errorf("Failed to create installation token: %s", err.Error())
	}
	ts = oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: installationToken.GetToken()},
	)
	tc = oauth2.NewClient(ctx, ts)
	client = github.NewClient(tc)
	return client, nil
}

func processIssueCommentEvent(
	ctx context.Context,
	hook *github.IssueCommentEvent) error {
	if hook.GetComment().GetBody() != "ping" {
		return nil
	}
	client, err := newGitHubClient(ctx, hook.GetInstallation().GetID())
	if err != nil {
		return err
	}
	_, _, err = client.Issues.CreateComment(
		ctx,
		hook.GetRepo().GetOwner().GetLogin(),
		hook.GetRepo().GetName(),
		hook.GetIssue().GetNumber(),
		&github.IssueComment{
			Body: &[]string{"pong"}[0],
		},
	)
	return err
}

type Response events.APIGatewayProxyResponse

func handler(event events.APIGatewayProxyRequest) (Response, error) {
	ctx := context.Background()
	if err := github.ValidateSignature(event.Headers["X-Hub-Signature"], []byte(event.Body), []byte(webhookSecret)); err != nil {
		logger.Info("Signature is invalid", zap.Error(err))
		return Response{
			StatusCode: http.StatusBadRequest,
		}, nil
	}
	hook, err := github.ParseWebHook(event.Headers["X-GitHub-Event"], []byte(event.Body))
	if err != nil {
		logger.Error("Failed to parse hook", zap.Error(err))
		return Response{
			StatusCode: http.StatusBadRequest,
		}, nil
	}
	switch hook := hook.(type) {
	case *github.IssueCommentEvent:
		err = processIssueCommentEvent(ctx, hook)
	}
	if err != nil {
		logger.Error("Failed to process an event", zap.Error(err))
		return Response{
			StatusCode: http.StatusInternalServerError,
		}, err
	}
	return Response{
		StatusCode: http.StatusOK,
	}, nil
}

func main() {
	initLogger()
	lambda.Start(handler)
}
