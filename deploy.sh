#!/bin/bash

go build .
mv lego-llm-assistant ~/

sudo systemctl daemon-reload
sudo systemctl restart lego-maya.service
