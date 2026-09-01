import * as THREE from 'three';

export interface VehicleDims {
  length: number;
  width: number;
  height: number;
}

export const dims: VehicleDims = {
  length: 5.1,
  width: 1.95,
  height: 1.75,
};

const BODY_WIDTH = 1.8;
const CABIN_WIDTH = 1.84;
const WHEEL_X = 0.855;
const WHEEL_Z = 1.55;
const WHEEL_RADIUS = 0.34;
const WHEEL_WIDTH = 0.24;
const HUB_RADIUS = 0.17;
const HUB_THICKNESS = 0.02;

export function build(): THREE.Group {
  const root = new THREE.Group();

  const bodyMaterial = new THREE.MeshStandardMaterial({
    color: 0x5f8dff,
    flatShading: true,
  });

  const cabinMaterial = new THREE.MeshStandardMaterial({
    color: 0x1d2950,
    flatShading: true,
    side: THREE.DoubleSide,
  });

  const chassisMaterial = new THREE.MeshStandardMaterial({
    color: 0x0a1230,
    flatShading: true,
  });

  const wheelMaterial = new THREE.MeshStandardMaterial({
    color: 0x101018,
    flatShading: true,
  });

  const hubMaterial = new THREE.MeshStandardMaterial({
    color: 0x5c6ba8,
    flatShading: true,
  });

  const headlightMaterial = new THREE.MeshStandardMaterial({
    color: 0xfff3c4,
    emissive: 0xfff3c4,
    flatShading: true,
  });

  const tailLightMaterial = new THREE.MeshStandardMaterial({
    color: 0xe5484d,
    emissive: 0xe5484d,
    flatShading: true,
  });

  // Chassis / underbody slab.
  const chassisGeo = new THREE.BoxGeometry(1.6, 0.18, 5.0);
  const chassis = new THREE.Mesh(chassisGeo, chassisMaterial);
  chassis.position.y = 0.23;
  chassis.name = 'chassis';
  root.add(chassis);

  // Main body: extruded side profile, one-box volume. Short vertical nose,
  // gentle hood slope, steeper windshield, tall flat roof held to the tail.
  // Shape x is forward (+Z after rotation), shape y is height.
  const bodyShape = new THREE.Shape();
  bodyShape.moveTo(2.55, 0.32);
  bodyShape.lineTo(2.55, 0.72);
  bodyShape.lineTo(1.7, 1.05);
  bodyShape.lineTo(1.0, 1.72);
  bodyShape.lineTo(-2.15, 1.75);
  bodyShape.lineTo(-2.55, 1.62);
  bodyShape.lineTo(-2.55, 0.32);
  bodyShape.closePath();

  const bodyGeo = new THREE.ExtrudeGeometry(bodyShape, {
    depth: BODY_WIDTH,
    bevelEnabled: false,
  });
  bodyGeo.rotateY(-Math.PI / 2);
  bodyGeo.translate(BODY_WIDTH / 2, 0, 0);

  const body = new THREE.Mesh(bodyGeo, bodyMaterial);
  body.name = 'body';
  root.add(body);

  // Cabin: long even glass band, extruded slightly wider than the body so it
  // reads as windows wrapping the upper half. Bottom edge stays level.
  const cabinShape = new THREE.Shape();
  cabinShape.moveTo(1.7, 1.05);
  cabinShape.lineTo(1.05, 1.69);
  cabinShape.lineTo(-2.05, 1.69);
  cabinShape.lineTo(-2.45, 1.56);
  cabinShape.lineTo(-2.45, 1.05);
  cabinShape.closePath();

  const cabinGeo = new THREE.ExtrudeGeometry(cabinShape, {
    depth: CABIN_WIDTH,
    bevelEnabled: false,
  });
  cabinGeo.rotateY(-Math.PI / 2);
  cabinGeo.translate(CABIN_WIDTH / 2, 0, 0);

  const cabin = new THREE.Mesh(cabinGeo, cabinMaterial);
  cabin.name = 'cabin';
  root.add(cabin);

  // Wheels.
  const wheels = new THREE.Group();
  wheels.name = 'wheels';

  const wheelGeo = new THREE.CylinderGeometry(
    WHEEL_RADIUS,
    WHEEL_RADIUS,
    WHEEL_WIDTH,
    10
  );
  wheelGeo.rotateZ(Math.PI / 2);

  const hubGeo = new THREE.CylinderGeometry(
    HUB_RADIUS,
    HUB_RADIUS,
    HUB_THICKNESS,
    8
  );
  hubGeo.rotateZ(Math.PI / 2);

  const wheelPositions = [
    { x: -WHEEL_X, z: -WHEEL_Z },
    { x:  WHEEL_X, z: -WHEEL_Z },
    { x: -WHEEL_X, z:  WHEEL_Z },
    { x:  WHEEL_X, z:  WHEEL_Z },
  ];

  for (const pos of wheelPositions) {
    const wheel = new THREE.Mesh(wheelGeo, wheelMaterial);
    wheel.position.set(pos.x, WHEEL_RADIUS, pos.z);
    wheels.add(wheel);

    const outerX = pos.x > 0
      ? pos.x + WHEEL_WIDTH / 2 + HUB_THICKNESS / 2
      : pos.x - WHEEL_WIDTH / 2 - HUB_THICKNESS / 2;

    const hub = new THREE.Mesh(hubGeo, hubMaterial);
    hub.position.set(outerX, WHEEL_RADIUS, pos.z);
    wheels.add(hub);
  }

  root.add(wheels);

  // Front lights.
  const lightsFront = new THREE.Group();
  lightsFront.name = 'lights-front';

  const headlightGeo = new THREE.BoxGeometry(0.26, 0.12, 0.06);

  const headlightL = new THREE.Mesh(headlightGeo, headlightMaterial);
  headlightL.position.set(-0.62, 0.58, dims.length / 2 - 0.03);

  const headlightR = new THREE.Mesh(headlightGeo, headlightMaterial);
  headlightR.position.set(0.62, 0.58, dims.length / 2 - 0.03);

  lightsFront.add(headlightL, headlightR);
  root.add(lightsFront);

  // Rear lights: tall vertical clusters, a minivan cue.
  const lightsRear = new THREE.Group();
  lightsRear.name = 'lights-rear';

  const tailLightGeo = new THREE.BoxGeometry(0.12, 0.3, 0.06);

  const tailLightL = new THREE.Mesh(tailLightGeo, tailLightMaterial);
  tailLightL.position.set(-0.78, 0.95, -dims.length / 2 + 0.03);

  const tailLightR = new THREE.Mesh(tailLightGeo, tailLightMaterial);
  tailLightR.position.set(0.78, 0.95, -dims.length / 2 + 0.03);

  lightsRear.add(tailLightL, tailLightR);
  root.add(lightsRear);

  return root;
}
