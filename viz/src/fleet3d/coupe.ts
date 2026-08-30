import * as THREE from 'three';

export interface VehicleDims {
  length: number;
  width: number;
  height: number;
}

export const dims: VehicleDims = {
  length: 4.6,
  width: 1.85,
  height: 1.35,
};

const BODY_WIDTH = 1.7;
const CABIN_WIDTH = 1.54;
const WHEEL_X = 0.79;
const WHEEL_Z = 1.45;
const WHEEL_RADIUS = 0.3;
const WHEEL_WIDTH = 0.22;
const HUB_RADIUS = 0.15;
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

  // Chassis / underbody slab. Low ride: clears ground by 0.08 m.
  const chassisGeo = new THREE.BoxGeometry(1.45, 0.14, 4.5);
  const chassis = new THREE.Mesh(chassisGeo, chassisMaterial);
  chassis.position.y = 0.15;
  chassis.name = 'chassis';
  root.add(chassis);

  // Main lower body. Top (hood / deck line) sits at 0.58 m.
  const bodyGeo = new THREE.BoxGeometry(BODY_WIDTH, 0.36, dims.length);
  const body = new THREE.Mesh(bodyGeo, bodyMaterial);
  body.position.y = 0.22 + 0.18;
  body.name = 'body';
  root.add(body);

  // Cabin / glasshouse: extruded side profile. Pushed rearward so the hood
  // runs long; fastback rear slopes off the roof into a short decklid lip.
  const cabinShape = new THREE.Shape();
  cabinShape.moveTo(0.45, 0.58); // base of windshield
  cabinShape.lineTo(-0.55, 1.35); // windshield header
  cabinShape.lineTo(-0.95, 1.35); // roof rear
  cabinShape.lineTo(-1.35, 1.06); // fastback mid-slope
  cabinShape.lineTo(-2.05, 0.62); // decklid lip above the tail
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

  // Front lights: slim units set wide, low on the nose.
  const lightsFront = new THREE.Group();
  lightsFront.name = 'lights-front';

  const headlightGeo = new THREE.BoxGeometry(0.28, 0.08, 0.06);

  // Proud of the nose: at length/2 - 0.03 the front face was coplanar with
  // the body's +Z face and z-fought.
  const headlightL = new THREE.Mesh(headlightGeo, headlightMaterial);
  headlightL.position.set(-0.58, 0.4, dims.length / 2 + 0.01);

  const headlightR = new THREE.Mesh(headlightGeo, headlightMaterial);
  headlightR.position.set(0.58, 0.4, dims.length / 2 + 0.01);

  lightsFront.add(headlightL, headlightR);
  root.add(lightsFront);

  // Rear lights: full-width light bar across the tail.
  const lightsRear = new THREE.Group();
  lightsRear.name = 'lights-rear';

  const tailLightGeo = new THREE.BoxGeometry(1.3, 0.07, 0.06);

  const tailLight = new THREE.Mesh(tailLightGeo, tailLightMaterial);
  tailLight.position.set(0, 0.44, -dims.length / 2 - 0.01);

  lightsRear.add(tailLight);
  root.add(lightsRear);

  return root;
}
