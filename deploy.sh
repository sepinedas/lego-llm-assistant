#!/bin/bash

go build .
mv lego-llm-assistant ~/

systemctl --user daemon-reload
systemctl --user restart lego-maya.service
