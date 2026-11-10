# Changelog

## [0.1.2](https://github.com/joshwrightcodes/bk-agent-stack-orchard/compare/v0.1.1...v0.1.2) (2026-11-10)


### Fixes

* **deps:** update module golang.org/x/sys to v0.50.0 ([#212](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/212)) ([0d9ad78](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/0d9ad7868ba29403b75f9ba5decd06d96f74039c))

## [0.1.1](https://github.com/joshwrightcodes/bk-agent-stack-orchard/compare/v0.1.0...v0.1.1) (2026-11-04)


### Fixes

* **telemetry:** keep the log records that come after a signal ([#197](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/197)) ([87105c7](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/87105c7699c8cfec953da1cb2565005413444b49))

## 0.1.0 (2026-11-04)


### Features

* **admission:** admit on fresh state, place waiting VMs as Orchard does ([#64](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/64)) ([4a9b049](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/4a9b0490577a2e502277f61d24be37b53dbe447c))
* **admission:** count the fleet room and choose the jobs to start ([#42](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/42)) ([548218c](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/548218ca05743a130fb7b41b041f274b89eb2bb5))
* **buildkite:** talk to the Stacks API through a narrow interface ([#35](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/35)) ([0f1a503](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/0f1a503b02d3ef05012ec60178b012a92e1f8ba9))
* **config:** read and validate the controller configuration ([#31](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/31)) ([3ae32a6](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/3ae32a65ad19c9732cb7c66d5cbb6303f7bb545e))
* **controller:** admit after the first job states, and prove restarts ([#68](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/68)) ([b01fdfe](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/b01fdfe70475ca73d04001ec2adf7654c2fac742))
* **controller:** run the tick that registers the stack and polls ([#41](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/41)) ([3b758e1](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/3b758e1e5a18572813498d54d094d4ea15dbbd0c))
* **deploy:** add a Helm chart for the controller ([#85](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/85)) ([3e576d3](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/3e576d3ffd64a10c46908921978dcd2de2e7c131))
* **deploy:** run the Orchard controller on Kubernetes ([#88](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/88)) ([057cad1](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/057cad17fb3d3f9c81a9902d8463a75fa27c756c))
* **health:** serve liveness and readiness over gRPC health checking ([#75](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/75)) ([5b0301f](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/5b0301f2479e81fcac3bc3cabdaa6e880ef4ee56))
* **hosts:** keep the job VMs away from their host with pf ([#101](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/101)) ([016e33a](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/016e33ae41edbef52985da194404fb38363c4348))
* **hosts:** make a Mac host an Orchard worker with Ansible ([#96](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/96)) ([bb4963b](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/bb4963b8146a7da675c5c7ec6ee90c4d55ba20aa))
* **hosts:** move to Softnet 0.24 and Tart 2.40, and block by address ([#107](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/107)) ([b162088](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/b162088bc1a654b284137103f0d5e6bab5f8dbd8))
* **images:** build an agent image without Xcode ([#94](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/94)) ([0f83ca0](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/0f83ca0e6548d02e66d0487bcd8d6086cca2ecc2))
* **images:** build the agent image from the vanilla macOS image ([#115](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/115)) ([0c3e1fd](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/0c3e1fd9bdc092fc1b9d6fdf7fe159aa0c61f593))
* **images:** build the pilot image from the vanilla macOS image ([#116](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/116)) ([6acf9ac](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/6acf9acc2d153cc8893135008b83aef187c15c1f))
* **jobstates:** record each job state with the time that it was asked ([#50](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/50)) ([ee1f154](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/ee1f1544fdef21c9a83deb53e683a3e34c50acf0))
* **launcher:** reserve a job, get its token, and start its machine ([#43](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/43)) ([3cc1819](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/3cc181993d13a5571ee83ae6e6a80301c24645e1))
* **match:** select a profile from the agent tags of a job ([#33](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/33)) ([b7e2486](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/b7e248608e8ab754a4b119a60fb776dcba70ed47)), closes [#5](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/5)
* **orchard:** call the Orchard API with a generated client ([#34](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/34)) ([5268cd6](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/5268cd677ede6399a64009984a41ab9a4a6b72d1))
* **progress:** show each step of a VM on its job page ([#79](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/79)) ([ad825f0](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/ad825f097d6214e152658bf19f8dee7cccf9b211))
* **reaper:** carry out every action of the decision table ([#56](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/56)) ([0439b21](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/0439b21b18b37e6a42860c7f06863eff2a04197e))
* **reaper:** delete a machine when its job ends or it lives too long ([#46](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/46)) ([6f53fd3](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/6f53fd34fbf4b47415ee40fc58e5b55db266fb8c))
* **reaper:** extend every reservation of a tick in one call ([#58](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/58)) ([ed426ed](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/ed426edb364b7fd12db49a9f0ed3c03cf3db18a0))
* **reaper:** read all thirteen rows of the decision table ([#55](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/55)) ([a8f5c63](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/a8f5c6341ccc5c8f717df6c0f0a7bfd409a74658))
* **reaper:** tell the job page why the controller ended the job ([#57](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/57)) ([ca60056](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/ca600560c03895165a2f8efedf998ac9ed9f5020))
* **telemetry:** count API errors, and complete the job and tick traces ([#78](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/78)) ([e896919](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/e89691910b5f1e121d047ac9805bc829d6026f2f))
* **telemetry:** export OpenTelemetry metrics, traces, and logs ([#36](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/36)) ([351b920](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/351b92036bc45f830b1f2546709f3b8a6bb20341))
* **vmspec:** build the specification of a job virtual machine ([#32](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/32)) ([19e56d2](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/19e56d2431876f3d5d01f7af5cf6fc52dfe4b623))
* **vmspec:** download the agent when the vm starts ([#156](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/156)) ([f0f7c66](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/f0f7c666e5108681e102266f5a5ce3899cdb94d9))
* **vmspec:** name the host after the vm and add image tags ([#152](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/152)) ([4f6dbc6](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/4f6dbc6d85a4ff100aa8b2f9b31793a67cb6f2d3))


### Fixes

* bound api calls so a slow service cannot stall the loop ([#177](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/177)) ([6096700](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/60967003c4cfcc1bad1089dc4288ee689999263e))
* **ci:** accept the capital letter of a Dependabot description ([#93](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/93)) ([3a6fcf7](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/3a6fcf76c4157f4f774ac3a28f36e8f85b73f01b))
* **ci:** check only the commits of the pull request ([#132](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/132)) ([9e8c434](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/9e8c4340efbb3714fa989546fb2765078c5fd975))
* **ci:** move Orchard to 0.58.0, and run a verified ad hoc copy of its binary ([#130](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/130)) ([970307e](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/970307e02ac2ab3cac4e6d1fa9fa11dfdfc7d92e))
* **ci:** stop the processes that a cancelled run left on the runner ([#99](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/99)) ([e9889ee](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/e9889eeb5a1eda3a232b3902bf82dc35dec838c7))
* **ci:** watch the pinned bases of the images, not their absence ([#113](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/113)) ([8decfa6](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/8decfa6604b9ecd837708ab7a891ea3e68475316))
* close the gaps of the fix stack review ([#185](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/185)) ([4d7d85b](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/4d7d85b830bc5168a5698da739c921b402c83d39))
* **controller:** start each job once, and read each source on time ([#48](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/48)) ([85ca71a](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/85ca71a5d3a66836509343a262ae69939a240dbe))
* **deps:** move golang.org/x/net to 0.62.0 ([#134](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/134)) ([ed8cff3](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/ed8cff33a00b8158d4d44a9d0a29dc850e7218da))
* **deps:** update go minor and patch ([#148](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/148)) ([c778171](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/c77817101e87dcbae84a4e98eebfc8e32d9a6f04))
* **deps:** update module google.golang.org/grpc to v1.85.0 ([#128](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/128)) ([a1c3e52](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/a1c3e52e46c5e25959a924fc871cc1262182cbc8))
* harden the controller for a first release ([#146](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/146)) ([4ccba8e](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/4ccba8e2c61e0ca32d5d15d9b702597824349dd1))
* harden the probes, orchard trust, and deploy egress ([#180](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/180)) ([840b876](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/840b876226110905375b3dd0742c1c2e837d7d9e))
* **hosts:** drain a host before a change, prune unused images ([#192](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/192)) ([a3614db](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/a3614dbb689b1a9923197b6015e72901e64ce6ba))
* **hosts:** keep the mac host closed to every job vm ([#179](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/179)) ([167a46c](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/167a46ceca896635a7d77e90cd4a165255a3a779))
* **hosts:** run tart by its path in the image tasks ([#194](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/194)) ([ae4b6c6](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/ae4b6c6d408e064c650a40113996419ce812d173))
* **images:** agent 4.9.0, macOS 27.2 everywhere, Tart 2.41.1 ([#140](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/140)) ([6ea6209](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/6ea620969d2906bd05b71641beab8c01f277390a))
* **images:** pass the release gate on the pilot image ([#151](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/151)) ([d1db998](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/d1db99878eb9ee78112caa5102796abe65076e67))
* **inventory:** decide that a worker is offline by the injected clock ([#45](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/45)) ([055b771](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/055b77185d8d3ae223f60d3c999f1d384d76330d))
* **inventory:** never treat another controller's vms as orphans ([#175](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/175)) ([164a28d](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/164a28dfc3867dd576945ca84a1a85771341b87b))
* keep orchard internal and give each host its own account ([#150](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/150)) ([c34e01e](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/c34e01eea8841434fafb41f5eab4e1882dde9ffa))
* **launcher:** give each token the start timeouts of its profile ([#188](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/188)) ([aaeb36c](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/aaeb36c4f5f9e56b1e368f5082d92c9778fdd76c))
* make the I-2 check and admission order deterministic ([#193](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/193)) ([4c11f7c](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/4c11f7c603dcebec4e18aa289933e1ed444cbd42))
* **orchard:** trust the Orchard certificate that the operator gives ([#86](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/86)) ([f3c60c8](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/f3c60c83dd2cb19528f10e2b584eefac7df7b195))
* **quickstart:** run orchard on loopback with authentication ([#176](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/176)) ([704fc75](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/704fc759768a7852a57847a300cf0e73f2b25843))
* **reaper:** close the reaper and token gaps of the review ([#191](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/191)) ([fb2a5f0](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/fb2a5f07a6b5ba3e1b0008000d058bafae161a5e))
* **reaper:** show the startup script of a machine that failed ([#103](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/103)) ([5c0fa24](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/5c0fa241e14475d91e486dacd66022074d042b6f))
* refuse a release whose commit is not on main ([#165](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/165)) ([c6a523c](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/c6a523c0b3c81068deb31620e08c0ad3cbf888fd))
* refuse plain HTTP endpoints, and patch Go for two vulns ([#138](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/138)) ([481b735](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/481b735aa8096787cf931b27c3f7066a4a31d9c7))
* run the creates and deletes of a tick in parallel ([#183](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/183)) ([3c53241](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/3c53241c97a27d9c68cbd6bdd3414cd21c15992c))
* **runner:** load the runner service again whenever it is not loaded ([#108](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/108)) ([026e528](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/026e52809ff9db72e3c270a5728ab23a89e85d36))
* **runner:** start the runner again after it stops ([#89](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/89)) ([4aa6e21](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/4aa6e219f5cccd7924d2ccade05e417ba9aba538))
* **scripts:** fix the correctness findings of the script review ([#170](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/170)) ([c2ca3c9](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/c2ca3c96f344e95de1adc02497b22d3f0390c58f))
* **stack:** sweep the queue behind a long head of unmatched jobs ([#178](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/178)) ([92b8541](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/92b854182f506d1aa9dcdcf65dd07f383f05fadb))
* **telemetry:** close the telemetry gaps of the review ([#196](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/196)) ([cdd63ed](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/cdd63ed8ea6bdb19d68c3c6ca8e470931037b72f))
* test the invariants and measure the pilot exit criteria ([#149](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/149)) ([3f9549d](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/3f9549d56600a532c2c4d440148e4c9d0d11f867))
* **vmspec:** remove extra git credential helpers before the agent ([#157](https://github.com/joshwrightcodes/bk-agent-stack-orchard/issues/157)) ([3f615a9](https://github.com/joshwrightcodes/bk-agent-stack-orchard/commit/3f615a9b30d56c9f602dd4e7febc32403c9aedfb))
