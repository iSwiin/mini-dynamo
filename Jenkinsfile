pipeline {
  agent any

  options {
    timestamps()
  }

  stages {
    stage('Checkout') {
      steps {
        checkout scm
      }
    }

    stage('Go: fmt/vet/test') {
      steps {
        sh '''
          set -euxo pipefail
          go version
          test -z "$(gofmt -l .)"
          go vet ./...
          go test ./...
        '''
      }
    }

    stage('Docker build') {
      steps {
        sh '''
          set -euxo pipefail
          docker build -t mini-dynamo:ci .
        '''
      }
    }
  }
}
