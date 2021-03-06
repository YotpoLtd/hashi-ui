import React, { Component, PropTypes } from 'react'
import { connect } from 'react-redux'
import { List, ListItem } from 'material-ui/List';
import { Card, CardHeader, CardText } from 'material-ui/Card';
import FontIcon from 'material-ui/FontIcon'
import { FETCH_CONSUL_REGIONS, SET_CONSUL_REGION } from '../sagas/event'

class SelectConsulRegion extends Component {

  constructor (props) {
    super(props)
    this._onClick = this.handleChangeRegion.bind(this)
  }

  componentDidMount() {
    this.props.dispatch({ type: FETCH_CONSUL_REGIONS })
  }

  handleChangeRegion(region) {
    this.props.dispatch({
      type: SET_CONSUL_REGION,
      payload: region
    })
  }

  render() {
    return (
      <Card>
        <CardHeader title='Please select a consul datacenter' />
        <CardText>
          <List>
            { Object.keys(this.props.consulRegions).map(region => {
              const regionName = this.props.consulRegions[region]
              return (
                <ListItem
                  leftIcon={ <FontIcon className='material-icons'>public</FontIcon> }
                  primaryText={ regionName }
                  onTouchTap={ () => this._onClick(regionName) }
                />
              )
            })}
          </List>
        </CardText>
      </Card>
    )
  }
}

function mapStateToProps ({ consulRegions }) {
  return { consulRegions }
}

SelectConsulRegion.defaultProps = {
  consulRegions: []
}

SelectConsulRegion.propTypes = {
  consulRegions: PropTypes.array.isRequired,
  dispatch: PropTypes.func.isRequired,
}

export default connect(mapStateToProps)(SelectConsulRegion)
